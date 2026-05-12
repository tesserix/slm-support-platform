package otp

import "time"

// Challenge is an outstanding OTP challenge for an email. The actual code
// is never stored — only its SHA-256 hash — so a DB dump does not leak
// valid codes.
type Challenge struct {
	ID         string    `bson:"_id"`
	TenantID   string    `bson:"tenant_id"`
	StoreID    string    `bson:"store_id"`
	Email      string    `bson:"email"`
	Name       string    `bson:"name,omitempty"`
	CodeHash   string    `bson:"code_hash"`
	CreatedAt  time.Time `bson:"created_at"`
	ExpiresAt  time.Time `bson:"expires_at"`
	Attempts   int       `bson:"attempts"`
	MaxAttempts int      `bson:"max_attempts"`
	ConsumedAt *time.Time `bson:"consumed_at,omitempty"`
}

// VerifyAudience is the kind of call asking for the verification — used
// so we can structurally separate "start a chat" from "send a gift" flows
// later. v1 only has one audience.
const AudienceChat = "chat"
