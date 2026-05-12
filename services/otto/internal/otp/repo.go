package otp

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ErrNotFound is returned when no outstanding challenge matches.
var ErrNotFound = errors.New("otp: not found")

// Repository persists OTP challenges with a TTL index so expired rows
// are cleaned up by Mongo without our help.
type Repository struct {
	coll *mongo.Collection
}

// NewRepository wires a Mongo collection + ensures indexes exist.
func NewRepository(ctx context.Context, coll *mongo.Collection) (*Repository, error) {
	r := &Repository{coll: coll}
	idx := []mongo.IndexModel{
		{
			Keys: bson.D{
				{Key: "tenant_id", Value: 1},
				{Key: "store_id", Value: 1},
				{Key: "email", Value: 1},
				{Key: "created_at", Value: -1},
			},
			Options: options.Index().SetName("tenant_store_email_latest"),
		},
		{
			// Mongo TTL: rows disappear when expires_at passes.
			Keys:    bson.D{{Key: "expires_at", Value: 1}},
			Options: options.Index().SetName("ttl_expires_at").SetExpireAfterSeconds(0),
		},
	}
	if _, err := coll.Indexes().CreateMany(ctx, idx); err != nil {
		return nil, err
	}
	return r, nil
}

// Insert stores a freshly minted challenge.
func (r *Repository) Insert(ctx context.Context, ch *Challenge) error {
	_, err := r.coll.InsertOne(ctx, ch)
	return err
}

// LatestFor returns the most recent non-consumed challenge for
// {tenant, store, email}. Used both for rate-limiting on /verify/start
// and for the "does a challenge exist" lookup on verify-consume.
func (r *Repository) LatestFor(ctx context.Context, tenantID, storeID, email string) (*Challenge, error) {
	filter := bson.M{
		"tenant_id":   tenantID,
		"store_id":    storeID,
		"email":       strings.ToLower(strings.TrimSpace(email)),
		"consumed_at": bson.M{"$exists": false},
	}
	opts := options.FindOne().SetSort(bson.D{{Key: "created_at", Value: -1}})
	var ch Challenge
	if err := r.coll.FindOne(ctx, filter, opts).Decode(&ch); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &ch, nil
}

// IncAttempt bumps the attempts counter atomically and returns the new
// value. When attempts reach MaxAttempts the caller should treat the
// challenge as spent and require a fresh start call.
func (r *Repository) IncAttempt(ctx context.Context, id string) (int, error) {
	var updated Challenge
	opts := options.FindOneAndUpdate().SetReturnDocument(options.After)
	err := r.coll.FindOneAndUpdate(ctx,
		bson.M{"_id": id},
		bson.M{"$inc": bson.M{"attempts": 1}},
		opts,
	).Decode(&updated)
	if err != nil {
		return 0, err
	}
	return updated.Attempts, nil
}

// MarkConsumed flags the challenge as used. Subsequent lookups return
// ErrNotFound, and the row is garbage-collected by the TTL index.
func (r *Repository) MarkConsumed(ctx context.Context, id string, at time.Time) error {
	_, err := r.coll.UpdateOne(ctx,
		bson.M{"_id": id},
		bson.M{"$set": bson.M{"consumed_at": at}},
	)
	return err
}
