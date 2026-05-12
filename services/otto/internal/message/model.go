package message

import "time"

// SenderType distinguishes who wrote a message. System messages come from
// the service itself (e.g. "Sara joined the chat").
type SenderType string

const (
	SenderCustomer SenderType = "customer"
	SenderStaff    SenderType = "staff"
	SenderSystem   SenderType = "system"
)

// Message is one line in a conversation. Stored in its own collection
// (not embedded) so threads can grow without bloating the parent document.
type Message struct {
	ID             string     `bson:"_id" json:"id"`
	ConversationID string     `bson:"conversation_id" json:"conversation_id"`
	TenantID       string     `bson:"tenant_id" json:"tenant_id"`
	StoreID        string     `bson:"store_id" json:"store_id"`
	SenderType     SenderType `bson:"sender_type" json:"sender_type"`
	SenderID       string     `bson:"sender_id,omitempty" json:"sender_id,omitempty"`
	SenderName     string     `bson:"sender_name,omitempty" json:"sender_name,omitempty"`
	Body           string     `bson:"body" json:"body"`
	CreatedAt      time.Time  `bson:"created_at" json:"created_at"`
}
