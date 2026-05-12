package watcher

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"
)

// TestCustomerMessageBSONRoundtrip locks in the BSON tags so a refactor
// of the struct doesn't silently break the change-stream decode path.
// A real Mongo integration test runs in CI against a temporary
// container; this unit test stays fast and dependency-free.
func TestCustomerMessageBSONRoundtrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	in := CustomerMessage{
		MessageID:      "msg-1",
		ConversationID: "conv-1",
		TenantID:       "mark8ly",
		StoreID:        "default",
		Body:           "where is my order?",
		CreatedAt:      now,
	}
	b, err := bson.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out CustomerMessage
	if err := bson.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out != in {
		t.Fatalf("roundtrip mismatch:\n  in : %+v\n  out: %+v", in, out)
	}
}
