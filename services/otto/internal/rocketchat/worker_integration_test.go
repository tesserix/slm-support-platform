//go:build integration

package rocketchat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/tesserix/slm-support-platform/services/otto/internal/conversation"
	"github.com/tesserix/slm-support-platform/services/otto/internal/message"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

func TestSupportRoundTripIsScopedAndIdempotent(t *testing.T) {
	uri := os.Getenv("TEST_MONGO_URL")
	if uri == "" {
		t.Fatal("TEST_MONGO_URL required")
	}
	mc, err := mongo.Connect(t.Context(), options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	db := mc.Database("rocketchat_test_" + time.Now().Format("150405000000000"))
	t.Cleanup(func() { _ = db.Drop(context.Background()); _ = mc.Disconnect(context.Background()) })
	sent := map[string]Message{}
	var replies []Message
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/chat.getMessage":
			m, ok := sent[r.URL.Query().Get("msgId")]
			if !ok {
				w.WriteHeader(404)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": m})
		case "/api/v1/chat.sendMessage":
			var in struct {
				Message Message `json:"message"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			in.Message.User.ID = "bot"
			sent[in.Message.ID] = in.Message
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "message": in.Message})
		case "/api/v1/chat.getThreadMessages":
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "messages": replies})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	cfg := Config{URL: srv.URL, UserID: "bot", Token: "test-token", RoomID: "support", StaffIDs: []string{"staff"}, HTTP: srv.Client()}
	client, err := NewClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	worker := NewWorker(db, client, nil, nil)
	now := time.Now().UTC().Truncate(time.Millisecond)
	conv := conversation.Conversation{ID: "case", TenantID: "platform", StoreID: "default", ProductID: "mark8ly", Status: conversation.StatusPending, CreatedAt: now, Customer: conversation.Customer{Name: "Example @all", Email: "test@example.invalid"}}
	if _, err := db.Collection("conversations").InsertOne(t.Context(), conv); err != nil {
		t.Fatal(err)
	}
	msg := message.Message{ID: "customer-line", ConversationID: "case", TenantID: "platform", StoreID: "default", SenderType: message.SenderCustomer, Body: "Help with billing", CreatedAt: now}
	if _, err := db.Collection("messages").InsertOne(t.Context(), msg); err != nil {
		t.Fatal(err)
	}
	if err := worker.SyncConversation(t.Context(), "case"); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("want root and customer line, got %d", len(sent))
	}
	reply := Message{ID: "reply", RoomID: "support", ThreadID: rootID("case"), Text: "We can help", Timestamp: now.Add(time.Second)}
	reply.User.ID = "staff"
	reply.User.Name = "Support agent"
	wrong := reply
	wrong.ID = "cross-room"
	wrong.RoomID = "other-room"
	forged := reply
	forged.ID = "unapproved"
	forged.User.ID = "stranger"
	replies = []Message{reply, wrong, forged}
	for range 2 {
		if err := worker.SyncConversation(t.Context(), "case"); err != nil {
			t.Fatal(err)
		}
	}
	count, err := db.Collection("messages").CountDocuments(t.Context(), bson.M{"sender_type": "staff"})
	if err != nil || count != 1 {
		t.Fatalf("staff messages=%d err=%v", count, err)
	}
	var updated conversation.Conversation
	if err := db.Collection("conversations").FindOne(t.Context(), bson.M{"_id": "case"}).Decode(&updated); err != nil {
		t.Fatal(err)
	}
	if updated.MessageCount != 1 || updated.UnreadCountCustomer != 1 || updated.Status != conversation.StatusActive {
		t.Fatalf("reply counters/state: %+v", updated)
	}

	blocked := reply
	blocked.ID = "blocked-content"
	blocked.Text = "Contact me at private@example.invalid"
	edited := reply
	edited.ID = "edited"
	edited.EditedAt = &now
	replies = append(replies, blocked, edited)
	if err := worker.SyncConversation(t.Context(), "case"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.Collection("messages").CountDocuments(t.Context(), bson.M{"sender_type": "staff"}); err != nil || n != 1 {
		t.Fatalf("blocked replies leaked: %d %v", n, err)
	}
	if _, err := db.Collection("conversations").UpdateOne(t.Context(), bson.M{"_id": "case"}, bson.M{"$set": bson.M{"status": "closed"}}); err != nil {
		t.Fatal(err)
	}
	closed := reply
	closed.ID = "after-close"
	replies = append(replies, closed)
	if err := worker.SyncConversation(t.Context(), "case"); err != nil {
		t.Fatal(err)
	}
	if n, err := db.Collection("messages").CountDocuments(t.Context(), bson.M{"sender_type": "staff"}); err != nil || n != 1 {
		t.Fatalf("closed reply leaked: %d %v", n, err)
	}
	if _, err := db.Collection("conversations").UpdateOne(t.Context(), bson.M{"_id": "case"}, bson.M{"$set": bson.M{"product_id": "another-product"}}); err != nil {
		t.Fatal(err)
	}
	if err := worker.SyncConversation(t.Context(), "case"); err == nil {
		t.Fatal("cross-product conversation allowed")
	}
}
