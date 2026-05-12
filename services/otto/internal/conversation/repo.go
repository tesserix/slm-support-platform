package conversation

import (
	"context"
	"crypto/rand"
	"encoding/base32"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// GenerateCaseID returns a short human-readable reference like
// CS-260417-A1B2. The date segment makes old cases sort naturally; the
// random tail avoids collisions without requiring a DB lookup.
func GenerateCaseID() (string, error) {
	var buf [3]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	enc := strings.ToUpper(strings.TrimRight(base32.StdEncoding.EncodeToString(buf[:]), "="))
	// First four chars of enc keeps us at CS-YYMMDD-XXXX — plenty of
	// entropy (5^4 * 32^4-ish, collisions are very rare at v1 volume).
	if len(enc) > 4 {
		enc = enc[:4]
	}
	return "CS-" + time.Now().UTC().Format("060102") + "-" + enc, nil
}

// ErrNotFound is returned when a lookup has a valid scope but no row matches.
var ErrNotFound = errors.New("conversation: not found")

// Repository persists and queries conversations with mandatory tenant+store
// scoping. No method on this type will ever cross a tenant boundary — every
// read and write filter includes tenant_id + store_id.
type Repository struct {
	coll *mongo.Collection
}

func NewRepository(coll *mongo.Collection) *Repository { return &Repository{coll: coll} }

// Insert creates a new pending conversation.
func (r *Repository) Insert(ctx context.Context, c *Conversation) error {
	if c.ID == "" || c.TenantID == "" || c.StoreID == "" {
		return errors.New("conversation: id, tenant_id and store_id are required")
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	c.UpdatedAt = c.CreatedAt
	if c.LastMessageAt.IsZero() {
		c.LastMessageAt = c.CreatedAt
	}
	if c.Status == "" {
		c.Status = StatusPending
	}
	_, err := r.coll.InsertOne(ctx, c)
	return err
}

// GetByID enforces scope: a caller with tenant A cannot fetch a conversation
// belonging to tenant B even if they know the id.
func (r *Repository) GetByID(ctx context.Context, tenantID, storeID, id string) (*Conversation, error) {
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	var c Conversation
	if err := r.coll.FindOne(ctx, filter).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// LatestOpenForSession returns the most recently-updated non-closed
// conversation belonging to a given customer session cookie. Used for the
// widget's "resume on reload" path so a customer who refreshes the page
// keeps their thread. Returns ErrNotFound if the session has no open
// thread in this {tenant, store} scope.
func (r *Repository) LatestOpenForSession(ctx context.Context, tenantID, storeID, sessionToken string) (*Conversation, error) {
	filter := bson.M{
		"tenant_id":              tenantID,
		"store_id":               storeID,
		"customer.session_token": sessionToken,
		"status":                 bson.M{"$ne": StatusClosed},
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "last_message_at", Value: -1}})
	var c Conversation
	if err := r.coll.FindOne(ctx, filter, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// GetForCustomer looks up a conversation a specific anonymous session is
// allowed to see. The session_token match is the customer-side auth check —
// without it, a malicious customer could poll arbitrary ids.
func (r *Repository) GetForCustomer(ctx context.Context, tenantID, storeID, id, sessionToken string) (*Conversation, error) {
	filter := bson.M{
		"_id":                    id,
		"tenant_id":              tenantID,
		"store_id":               storeID,
		"customer.session_token": sessionToken,
	}
	var c Conversation
	if err := r.coll.FindOne(ctx, filter).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListInbox returns conversations for a staff inbox, filtered by status and
// optionally by assignee. Always scoped to tenant+store.
type ListInboxParams struct {
	TenantID        string
	StoreID         string
	Status          Status // empty = all
	AssigneeUserID  string // empty = any assignee
	OnlyUnassigned  bool
	Limit           int64
}

func (r *Repository) ListInbox(ctx context.Context, p ListInboxParams) ([]Conversation, error) {
	filter := bson.M{"tenant_id": p.TenantID, "store_id": p.StoreID}
	if p.Status != "" {
		filter["status"] = p.Status
	}
	switch {
	case p.OnlyUnassigned:
		filter["assignee"] = bson.M{"$exists": false}
	case p.AssigneeUserID != "":
		filter["assignee.user_id"] = p.AssigneeUserID
	}
	limit := p.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "last_message_at", Value: -1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Conversation
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Accept assigns a staff member to the conversation and flips status to
// active. Idempotent-ish: if someone else already accepted, it returns the
// current state without error — the UI can display who's on it.
func (r *Repository) Accept(ctx context.Context, tenantID, storeID, id string, assignee Assignee) (*Conversation, error) {
	now := time.Now().UTC()
	if assignee.AssignedAt.IsZero() {
		assignee.AssignedAt = now
	}
	filter := bson.M{
		"_id":       id,
		"tenant_id": tenantID,
		"store_id":  storeID,
		"status":    StatusPending,
	}
	update := bson.M{
		"$set": bson.M{
			"status":     StatusActive,
			"assignee":   assignee,
			"updated_at": now,
		},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Either not-our-tenant or already accepted — re-read to tell them apart.
			existing, getErr := r.GetByID(ctx, tenantID, storeID, id)
			if getErr != nil {
				return nil, getErr
			}
			return existing, nil
		}
		return nil, err
	}
	return &c, nil
}

// Reopen flips a closed conversation back to its last meaningful state.
// We pick `active` when an assignee is still attached, otherwise `pending`
// so it lands back in the new-threads queue for any staff to pick up.
func (r *Repository) Reopen(ctx context.Context, tenantID, storeID, id string) (*Conversation, error) {
	now := time.Now().UTC()
	current, err := r.GetByID(ctx, tenantID, storeID, id)
	if err != nil {
		return nil, err
	}
	nextStatus := StatusPending
	if current.Assignee != nil && current.Assignee.UserID != "" {
		nextStatus = StatusActive
	}
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	update := bson.M{
		"$set": bson.M{
			"status":     nextStatus,
			"updated_at": now,
		},
		"$unset": bson.M{"closed_at": ""},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// Close flips a conversation to closed. Both customer and staff may trigger
// this but the HTTP layer decides who's allowed.
func (r *Repository) Close(ctx context.Context, tenantID, storeID, id string) (*Conversation, error) {
	now := time.Now().UTC()
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	update := bson.M{
		"$set": bson.M{
			"status":     StatusClosed,
			"closed_at":  now,
			"updated_at": now,
		},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// BumpOnMessage updates the counters + timestamps whenever a message lands.
// The audience argument indicates who the message was *not* read by so we can
// advance the correct unread counter.
type Audience string

const (
	AudienceStaff    Audience = "staff"
	AudienceCustomer Audience = "customer"
)

func (r *Repository) BumpOnMessage(ctx context.Context, tenantID, storeID, id string, unreadFor Audience, at time.Time) error {
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	inc := bson.M{"message_count": 1}
	switch unreadFor {
	case AudienceStaff:
		inc["unread_count_staff"] = 1
	case AudienceCustomer:
		inc["unread_count_customer"] = 1
	}
	update := bson.M{
		"$set": bson.M{"last_message_at": at, "updated_at": at},
		"$inc": inc,
	}
	_, err := r.coll.UpdateOne(ctx, filter, update)
	return err
}

// ClearUnread resets the unread counter for the given audience — called when
// the corresponding UI marks the thread as read.
func (r *Repository) ClearUnread(ctx context.Context, tenantID, storeID, id string, audience Audience) error {
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	field := "unread_count_customer"
	if audience == AudienceStaff {
		field = "unread_count_staff"
	}
	_, err := r.coll.UpdateOne(ctx, filter, bson.M{"$set": bson.M{field: 0}})
	return err
}

// QueueSnapshot answers the widget's "connecting…" screen: how many
// pending threads sit ahead of this one, and how many are pending in
// total. Position is 1-indexed ("you are #3") so position == 1 means
// they're next. A position of 0 means this conversation is already
// active / closed and shouldn't be rendering the queue UI.
type QueueSnapshot struct {
	Position      int `json:"position"`
	TotalPending  int `json:"total_pending"`
	EstimatedWait int `json:"estimated_wait_seconds"`
}

// QueuePosition computes where a given conversation sits in the FIFO
// pending queue, and the estimated wait based on a rough constant
// per-case handle time. Phase 1 uses a fixed 180s/case; phase 2 will
// plug in rolling-average handle time once staff availability ships.
func (r *Repository) QueuePosition(ctx context.Context, tenantID, storeID, id string) (QueueSnapshot, error) {
	self, err := r.GetByID(ctx, tenantID, storeID, id)
	if err != nil {
		return QueueSnapshot{}, err
	}
	if self.Status != StatusPending {
		return QueueSnapshot{Position: 0, TotalPending: 0, EstimatedWait: 0}, nil
	}
	base := bson.M{
		"tenant_id": tenantID,
		"store_id":  storeID,
		"status":    StatusPending,
	}
	total, err := r.coll.CountDocuments(ctx, base)
	if err != nil {
		return QueueSnapshot{}, err
	}
	ahead, err := r.coll.CountDocuments(ctx, bson.M{
		"tenant_id":  tenantID,
		"store_id":   storeID,
		"status":     StatusPending,
		"created_at": bson.M{"$lt": self.CreatedAt},
	})
	if err != nil {
		return QueueSnapshot{}, err
	}
	const avgHandleSeconds = 180
	return QueueSnapshot{
		Position:      int(ahead) + 1,
		TotalPending:  int(total),
		EstimatedWait: int(ahead) * avgHandleSeconds,
	}, nil
}

// SubmitFeedback attaches the post-case survey. Only closed
// conversations accept feedback, and only once — we no-op on a second
// submission rather than overwriting the customer's first answer.
func (r *Repository) SubmitFeedback(ctx context.Context, tenantID, storeID, id string, fb Feedback) (*Conversation, error) {
	now := time.Now().UTC()
	if fb.SubmittedAt.IsZero() {
		fb.SubmittedAt = now
	}
	filter := bson.M{
		"_id":       id,
		"tenant_id": tenantID,
		"store_id":  storeID,
		"status":    StatusClosed,
		"feedback":  bson.M{"$exists": false},
	}
	update := bson.M{
		"$set": bson.M{
			"feedback":   fb,
			"updated_at": now,
		},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			// Either not-closed, or already has feedback. Return the
			// current state so the handler can decide which is which.
			existing, getErr := r.GetByID(ctx, tenantID, storeID, id)
			if getErr != nil {
				return nil, getErr
			}
			return existing, nil
		}
		return nil, err
	}
	return &c, nil
}

// BumpCustomerActivity records the last time the customer pressed
// send, separate from UpdatedAt which bumps on any action. The
// phase-2 inactivity sweeper watches this field only; staff messages
// don't count because the point is to close cases where the customer
// walked away.
func (r *Repository) BumpCustomerActivity(ctx context.Context, tenantID, storeID, id string, at time.Time) error {
	filter := bson.M{"_id": id, "tenant_id": tenantID, "store_id": storeID}
	update := bson.M{"$set": bson.M{"last_customer_message_at": at}}
	_, err := r.coll.UpdateOne(ctx, filter, update)
	return err
}

// PopNextPending atomically grabs the oldest pending conversation
// and assigns it to the given staff member. Used by the
// accept-next endpoint — gated on status=pending so two concurrent
// "Accept Next" clicks can never land on the same case (Mongo's
// findOneAndUpdate is atomic per-document, which is exactly what
// FIFO needs).
//
// Returns ErrNotFound when there are no pending cases to pop.
func (r *Repository) PopNextPending(ctx context.Context, tenantID, storeID string, assignee Assignee) (*Conversation, error) {
	now := time.Now().UTC()
	if assignee.AssignedAt.IsZero() {
		assignee.AssignedAt = now
	}
	filter := bson.M{
		"tenant_id": tenantID,
		"store_id":  storeID,
		"status":    StatusPending,
	}
	update := bson.M{
		"$set": bson.M{
			"status":     StatusActive,
			"assignee":   assignee,
			"updated_at": now,
		},
	}
	opts := options.FindOneAndUpdate().
		SetSort(bson.D{{Key: "created_at", Value: 1}}).
		SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}

// ListInactive returns active conversations whose last customer
// message is older than `cutoff`. Used by the inactivity sweeper to
// batch-close abandoned cases. Scoping is deliberately global — the
// sweeper runs once per service and processes every tenant.
func (r *Repository) ListInactive(ctx context.Context, cutoff time.Time, limit int64) ([]Conversation, error) {
	if limit <= 0 {
		limit = 200
	}
	filter := bson.M{
		"status": StatusActive,
		"last_customer_message_at": bson.M{
			"$lt":     cutoff,
			"$exists": true,
		},
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "last_customer_message_at", Value: 1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Conversation
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CloseForInactivity flips the conversation to closed and stamps
// InactivityClosedAt so the customer sees a slightly different closed
// banner ("Case auto-closed — no reply for 15 min"). Gated on
// status=active so a staff-initiated close + sweeper race still
// produces one close, not two.
func (r *Repository) CloseForInactivity(ctx context.Context, tenantID, storeID, id string) (*Conversation, error) {
	now := time.Now().UTC()
	filter := bson.M{
		"_id":       id,
		"tenant_id": tenantID,
		"store_id":  storeID,
		"status":    StatusActive,
	}
	update := bson.M{
		"$set": bson.M{
			"status":               StatusClosed,
			"closed_at":            now,
			"updated_at":           now,
			"inactivity_closed_at": now,
		},
	}
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	var c Conversation
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}
