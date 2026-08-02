package message

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Repository persists and lists messages scoped to a conversation within
// a tenant+store. All queries enforce the triple.
type Repository struct {
	coll *mongo.Collection
}

func NewRepository(coll *mongo.Collection) *Repository { return &Repository{coll: coll} }

// storeScope builds the store_id clause for a READ filter. An empty store
// also matches rows where the field is absent — Insert has required store_id
// since it was added, but messages written before that guard carry none, and
// `store_id: ""` does not match a missing field in Mongo. Writes keep plain
// equality; this only widens a read within the caller's own tenant.
func storeScope(storeID string) any {
	if storeID == "" {
		return bson.M{"$in": bson.A{"", nil}}
	}
	return storeID
}

// Insert writes a new message.
func (r *Repository) Insert(ctx context.Context, m *Message) error {
	if m.ID == "" || m.ConversationID == "" || m.TenantID == "" || m.StoreID == "" {
		return errors.New("message: id, conversation_id, tenant_id, store_id required")
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	_, err := r.coll.InsertOne(ctx, m)
	return err
}

// ListByConversation returns messages in chronological order. Scope check
// is mandatory: mismatched tenant_id or store_id returns an empty slice.
func (r *Repository) ListByConversation(ctx context.Context, tenantID, storeID, convID string, limit int64) ([]Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	filter := bson.M{
		"conversation_id": convID,
		"tenant_id":       tenantID,
		"store_id":        storeScope(storeID),
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: 1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Message
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}
