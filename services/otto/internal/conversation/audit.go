package conversation

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// AuditAction enumerates the events we record to the otto_audit
// collection. Keep this list short and stable — downstream reporting
// pivots on the exact string values.
type AuditAction string

const (
	AuditCaseCreated        AuditAction = "case.created"
	AuditCaseAccepted       AuditAction = "case.accepted"
	AuditCaseAcceptedNext   AuditAction = "case.accepted_next"
	AuditCaseClosed         AuditAction = "case.closed"
	AuditCaseClosedByCust   AuditAction = "case.closed_by_customer"
	AuditCaseClosedInactive AuditAction = "case.closed_inactivity"
	AuditCaseReopened       AuditAction = "case.reopened"
	AuditFeedbackSubmitted  AuditAction = "feedback.submitted"
	AuditStaffAvailable     AuditAction = "staff.available"
	AuditStaffPaused        AuditAction = "staff.paused"
)

// Actor captures who triggered the event. For customer-driven events
// (case create, close, feedback) Type is "customer" and the user
// identity comes from the conversation.Customer record. For staff
// events Type is "staff" and ID/Name/Email come from the session.
// Type "system" covers the inactivity sweeper.
type Actor struct {
	Type  string `bson:"type"            json:"type"`
	ID    string `bson:"id,omitempty"    json:"id,omitempty"`
	Name  string `bson:"name,omitempty"  json:"name,omitempty"`
	Email string `bson:"email,omitempty" json:"email,omitempty"`
}

// AuditEvent is one row in the otto_audit collection. Scoped by
// tenant + store so queries can be filtered the same way every other
// Otto query is. Meta is deliberately bson.M (free-form) so we can
// record action-specific context (e.g., feedback ratings, queue
// position at accept time) without a schema migration per addition.
type AuditEvent struct {
	ID             string      `bson:"_id"                    json:"id"`
	TenantID       string      `bson:"tenant_id"              json:"tenant_id"`
	StoreID        string      `bson:"store_id"               json:"store_id"`
	ConversationID string      `bson:"conversation_id"        json:"conversation_id"`
	CaseID         string      `bson:"case_id,omitempty"      json:"case_id,omitempty"`
	Action         AuditAction `bson:"action"                 json:"action"`
	Actor          Actor       `bson:"actor"                  json:"actor"`
	Meta           bson.M      `bson:"meta,omitempty"         json:"meta,omitempty"`
	At             time.Time   `bson:"at"                     json:"at"`
}

// AuditRepository persists + queries the audit trail.
type AuditRepository struct {
	coll *mongo.Collection
}

func NewAuditRepository(coll *mongo.Collection) *AuditRepository {
	return &AuditRepository{coll: coll}
}

// Emit inserts an audit event. Errors are returned but callers
// typically log + swallow — audit failure should never block the
// primary action.
func (r *AuditRepository) Emit(ctx context.Context, e AuditEvent) error {
	if e.ID == "" {
		e.ID = uuid.NewString()
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if e.TenantID == "" || e.StoreID == "" {
		return errors.New("audit: tenant_id + store_id are required")
	}
	_, err := r.coll.InsertOne(ctx, e)
	return err
}

// ListByConversation returns the audit trail for a single case,
// oldest-first so the UI can render it as a timeline without a
// client-side sort. Scoping by tenant + store prevents cross-tenant
// leaks even if a caller knows a conversation id.
func (r *AuditRepository) ListByConversation(ctx context.Context, tenantID, storeID, conversationID string, limit int64) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	filter := bson.M{
		"tenant_id":       tenantID,
		"store_id":        storeID,
		"conversation_id": conversationID,
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "at", Value: 1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []AuditEvent
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ListRecent returns the most recent events for a store. Feeds a
// future "audit log" admin page — a short tail of every significant
// action across all cases in the store.
func (r *AuditRepository) ListRecent(ctx context.Context, tenantID, storeID string, limit int64) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	filter := bson.M{
		"tenant_id": tenantID,
		"store_id":  storeID,
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "at", Value: -1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []AuditEvent
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
