package conversation

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// StaffAvailability records whether a staff member is in the
// "available for next customer" pool. One document per (store, staff)
// pair. Documents live in the `staff_availability` collection.
//
// Lifecycle:
//   - staff toggles `available=true` → they join the pool
//   - they click Accept Next → server pops the oldest pending case,
//     assigns it to them, and records its id in CurrentCaseID
//   - when the case is closed / reassigned, CurrentCaseID goes back
//     to empty; availability persists unless the staff also paused
//
// The LastSeenAt field is bumped on every toggle/accept so a future
// admin dashboard can surface stale-but-still-available staff.
type StaffAvailability struct {
	ID            string     `bson:"_id"                        json:"id"`
	TenantID      string     `bson:"tenant_id"                  json:"tenant_id"`
	StoreID       string     `bson:"store_id"                   json:"store_id"`
	StaffID       string     `bson:"staff_id"                   json:"staff_id"`
	StaffName     string     `bson:"staff_name,omitempty"       json:"staff_name,omitempty"`
	StaffEmail    string     `bson:"staff_email,omitempty"      json:"staff_email,omitempty"`
	Available     bool       `bson:"available"                  json:"available"`
	CurrentCaseID string     `bson:"current_case_id,omitempty"  json:"current_case_id,omitempty"`
	LastSeenAt    time.Time  `bson:"last_seen_at"               json:"last_seen_at"`
	PausedAt      *time.Time `bson:"paused_at,omitempty"        json:"paused_at,omitempty"`
	UpdatedAt     time.Time  `bson:"updated_at"                 json:"updated_at"`
}

// AvailabilityRepository manages the staff_availability collection.
// Tenant + store scoping is mandatory on every read and write — the
// collection is shared across stores, the scope filters keep it safe.
type AvailabilityRepository struct {
	coll *mongo.Collection
}

func NewAvailabilityRepository(coll *mongo.Collection) *AvailabilityRepository {
	return &AvailabilityRepository{coll: coll}
}

func availabilityID(tenantID, storeID, staffID string) string {
	// Deterministic id so we can upsert without a separate lookup.
	return tenantID + ":" + storeID + ":" + staffID
}

// Get returns the current availability row for the given staff
// member. Missing rows come back as ErrNotFound so callers can
// distinguish "never toggled" from "genuinely unavailable".
func (r *AvailabilityRepository) Get(ctx context.Context, tenantID, storeID, staffID string) (*StaffAvailability, error) {
	filter := bson.M{
		"_id":       availabilityID(tenantID, storeID, staffID),
		"tenant_id": tenantID,
		"store_id":  storeID,
		"staff_id":  staffID,
	}
	var a StaffAvailability
	if err := r.coll.FindOne(ctx, filter).Decode(&a); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &a, nil
}

// SetAvailable upserts the row. When going available we clear any
// stale PausedAt; when pausing we stamp it for the UI badge.
func (r *AvailabilityRepository) SetAvailable(ctx context.Context, tenantID, storeID, staffID, staffName, staffEmail string, available bool) (*StaffAvailability, error) {
	now := time.Now().UTC()
	set := bson.M{
		"tenant_id":    tenantID,
		"store_id":     storeID,
		"staff_id":     staffID,
		"staff_name":   staffName,
		"staff_email":  staffEmail,
		"available":    available,
		"last_seen_at": now,
		"updated_at":   now,
	}
	update := bson.M{"$set": set}
	if available {
		update["$unset"] = bson.M{"paused_at": ""}
	} else {
		set["paused_at"] = now
	}
	filter := bson.M{"_id": availabilityID(tenantID, storeID, staffID)}
	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)
	var a StaffAvailability
	if err := r.coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&a); err != nil {
		return nil, err
	}
	return &a, nil
}

// ClaimCase atomically binds a case to a staff member. We gate on
// available=true AND current_case_id missing so the same staff can't
// pick up a second case while one's in flight. Returns ErrNotFound
// when the CAS fails — the caller re-reads to find out why.
func (r *AvailabilityRepository) ClaimCase(ctx context.Context, tenantID, storeID, staffID, caseID string) error {
	filter := bson.M{
		"_id":       availabilityID(tenantID, storeID, staffID),
		"tenant_id": tenantID,
		"store_id":  storeID,
		"staff_id":  staffID,
		"available": true,
		"$or": []bson.M{
			{"current_case_id": bson.M{"$exists": false}},
			{"current_case_id": ""},
		},
	}
	now := time.Now().UTC()
	update := bson.M{"$set": bson.M{
		"current_case_id": caseID,
		"last_seen_at":    now,
		"updated_at":      now,
	}}
	res, err := r.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrNotFound
	}
	return nil
}

// ReleaseCase clears the current-case binding. Availability is
// deliberately NOT flipped off — when a case ends the staff remains
// in the pool and is eligible for the next customer until they
// explicitly pause.
func (r *AvailabilityRepository) ReleaseCase(ctx context.Context, tenantID, storeID, staffID string) error {
	filter := bson.M{
		"_id":       availabilityID(tenantID, storeID, staffID),
		"tenant_id": tenantID,
		"store_id":  storeID,
		"staff_id":  staffID,
	}
	now := time.Now().UTC()
	update := bson.M{
		"$set":   bson.M{"last_seen_at": now, "updated_at": now},
		"$unset": bson.M{"current_case_id": ""},
	}
	_, err := r.coll.UpdateOne(ctx, filter, update)
	return err
}

// CountAvailable counts unique staff currently available AND without
// a case in flight. Used to render "all agents busy" on the customer
// side when the count hits zero while there are still pending cases.
func (r *AvailabilityRepository) CountAvailable(ctx context.Context, tenantID, storeID string) (int64, error) {
	filter := bson.M{
		"tenant_id": tenantID,
		"store_id":  storeID,
		"available": true,
		"$or": []bson.M{
			{"current_case_id": bson.M{"$exists": false}},
			{"current_case_id": ""},
		},
	}
	return r.coll.CountDocuments(ctx, filter)
}
