// Package watcher consumes Otto's MongoDB change stream and yields
// CustomerMessage events to the orchestrator.
//
// We watch the "messages" collection rather than "conversations" because
// inserts there are the unambiguous trigger: every new customer message
// is exactly one insert. Watching "conversations" updates would force us
// to diff the previous state to detect message arrival, which is more
// fragile.
//
// The watcher is event-driven (push to a channel); it does NOT poll.
package watcher

import "time"

// CustomerMessage is the single event type emitted by the watcher. It
// carries the minimum the orchestrator needs to look up the
// conversation and call downstream services. Larger fields (full
// conversation document, message history) are fetched on demand inside
// the orchestrator so this event stays small enough to drop cheaply
// when load spikes.
type CustomerMessage struct {
	MessageID      string    `bson:"_id"`
	ConversationID string    `bson:"conversation_id"`
	TenantID       string    `bson:"tenant_id"`
	StoreID        string    `bson:"store_id"`
	Body           string    `bson:"body"`
	CreatedAt      time.Time `bson:"created_at"`
}
