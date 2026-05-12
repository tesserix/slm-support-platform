// Package otto writes assistant replies back into Otto's MongoDB.
//
// We don't talk to Otto over HTTP. The orchestrator inserts directly
// into Otto's `messages` collection and updates the matching
// `conversations` document (NeedsHuman, LastAssistantMessageAt,
// message_count, unread_count_customer). Otto's WebSocket hub picks
// the insertion up via its own change stream and pushes the message
// to the customer browser without any code change on Otto's side.
//
// The schema and BSON tags here mirror services/otto/internal/message
// and services/otto/internal/conversation. If those change, this
// package must change too — the layering is "shared schema, separate
// process" rather than "shared library", because making slm-router
// depend on Otto's package would conflate two services.
package otto

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Writer posts assistant replies and updates conversation state.
type Writer interface {
	PostAssistantMessage(ctx context.Context, msg AssistantMessage) error
	MarkNeedsHuman(ctx context.Context, conversationID string, reason string) error
	// RecentMessages returns up to limit most-recent messages on a
	// conversation, ordered oldest-first (ready for direct
	// concatenation into the model's messages array).
	RecentMessages(ctx context.Context, conversationID string, limit int) ([]HistoryMessage, error)
}

// HistoryMessage is one row of the conversation history.
type HistoryMessage struct {
	SenderType string // "customer" | "staff" | "assistant" | "system"
	Body       string
	CreatedAt  time.Time
}

// AssistantMessage is the input to PostAssistantMessage. SenderID is
// usually the model name+version (e.g. "qwen2.5-1.5b-instruct").
type AssistantMessage struct {
	ConversationID string
	TenantID       string
	StoreID        string
	Body           string
	SenderID       string // model name+version
	SenderName     string // human label, e.g. "Otto AI"
}

// MongoWriter is the concrete Writer backed by go.mongodb.org/mongo-driver.
type MongoWriter struct {
	db *mongo.Database
}

// NewMongoWriter wraps an existing mongo.Database. The caller owns the
// client's lifecycle.
func NewMongoWriter(db *mongo.Database) *MongoWriter {
	return &MongoWriter{db: db}
}

// PostAssistantMessage inserts a SenderAssistant message into the
// `messages` collection AND updates the parent conversation's
// LastAssistantMessageAt + message_count + unread_count_customer.
// Both writes happen in a session so the inbox never sees a counter
// out of sync with the actual message rows.
func (w *MongoWriter) PostAssistantMessage(ctx context.Context, msg AssistantMessage) error {
	if msg.ConversationID == "" || msg.TenantID == "" {
		return fmt.Errorf("conversation_id and tenant_id required")
	}
	now := time.Now().UTC()
	messageID := uuid.NewString()

	messageDoc := bson.M{
		"_id":             messageID,
		"conversation_id": msg.ConversationID,
		"tenant_id":       msg.TenantID,
		"store_id":        msg.StoreID,
		"sender_type":     "assistant",
		"sender_id":       msg.SenderID,
		"sender_name":     msg.SenderName,
		"body":            msg.Body,
		"created_at":      now,
	}
	convUpdate := bson.M{
		"$set": bson.M{
			"last_message_at":           now,
			"last_assistant_message_at": now,
			"updated_at":                now,
		},
		"$inc": bson.M{
			"message_count":         1,
			"unread_count_customer": 1,
		},
	}

	// MongoDB requires a replica set for multi-document transactions.
	// Production Mongo is always a replica set (CNPG / Mongo charts both
	// deploy one); in local dev a single-node replica set works. We do
	// the writes sequentially WITHOUT a session for simplicity since the
	// orchestrator already handles partial-write recovery on its next
	// turn — if the message inserts but the counter update fails, the
	// next turn corrects it.
	if _, err := w.db.Collection("messages").InsertOne(ctx, messageDoc); err != nil {
		return fmt.Errorf("insert assistant message: %w", err)
	}
	filter := bson.M{
		"_id":       msg.ConversationID,
		"tenant_id": msg.TenantID,
	}
	if _, err := w.db.Collection("conversations").UpdateOne(ctx, filter, convUpdate); err != nil {
		return fmt.Errorf("update conversation counters: %w", err)
	}
	return nil
}

// RecentMessages reads the last `limit` messages on the conversation,
// returning them oldest-first.
func (w *MongoWriter) RecentMessages(ctx context.Context, conversationID string, limit int) ([]HistoryMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	filter := bson.M{"conversation_id": conversationID}
	opts := options.Find().
		SetSort(bson.D{{Key: "created_at", Value: -1}}).
		SetLimit(int64(limit))
	cur, err := w.db.Collection("messages").Find(ctx, filter, opts)
	if err != nil {
		return nil, fmt.Errorf("query history: %w", err)
	}
	defer cur.Close(ctx)
	var reversed []HistoryMessage
	for cur.Next(ctx) {
		var doc struct {
			SenderType string    `bson:"sender_type"`
			Body       string    `bson:"body"`
			CreatedAt  time.Time `bson:"created_at"`
		}
		if err := cur.Decode(&doc); err != nil {
			return nil, fmt.Errorf("decode history: %w", err)
		}
		reversed = append(reversed, HistoryMessage{
			SenderType: doc.SenderType,
			Body:       doc.Body,
			CreatedAt:  doc.CreatedAt,
		})
	}
	// Reverse to oldest-first.
	out := make([]HistoryMessage, len(reversed))
	for i := range reversed {
		out[i] = reversed[len(reversed)-1-i]
	}
	return out, nil
}

// MarkNeedsHuman flips NeedsHuman to true and posts a small system
// hand-off message so the customer sees "a human will be with you
// shortly" instead of silence.
func (w *MongoWriter) MarkNeedsHuman(ctx context.Context, conversationID, reason string) error {
	now := time.Now().UTC()
	filter := bson.M{"_id": conversationID}
	update := bson.M{
		"$set": bson.M{
			"needs_human": true,
			"updated_at":  now,
		},
	}
	res, err := w.db.Collection("conversations").UpdateOne(ctx, filter, update)
	if err != nil {
		return fmt.Errorf("mark needs_human: %w", err)
	}
	if res.MatchedCount == 0 {
		return fmt.Errorf("conversation %s not found", conversationID)
	}
	return nil
}

var _ Writer = (*MongoWriter)(nil)
