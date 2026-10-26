package rocketchat

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"time"
	"unicode"

	"github.com/tesserix/slm-support-platform/services/otto/internal/contentguard"
	"github.com/tesserix/slm-support-platform/services/otto/internal/conversation"
	"github.com/tesserix/slm-support-platform/services/otto/internal/event"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/message"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Worker uses committed Otto messages as its durable delivery source.
type Worker struct {
	db   *mongo.Database
	chat *Client
	hub  *hub.Hub
	log  *slog.Logger
}

// NewWorker binds the bridge to the existing database and live conversation hub.
func NewWorker(db *mongo.Database, chat *Client, h *hub.Hub, log *slog.Logger) *Worker {
	if log == nil {
		log = slog.Default()
	}
	return &Worker{db: db, chat: chat, hub: h, log: log}
}

func scope(id string) bson.M {
	return bson.M{"_id": id, "tenant_id": "platform", "store_id": "default", "product_id": "mark8ly"}
}
func rootID(id string) string { return "mark8ly-support-" + id }
func text(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	r := []rune(s)
	if len(r) > 4096 {
		r = r[:4096]
	}
	return strings.NewReplacer("@", "＠", "*", "\\*", "_", "\\_", "`", "'", "[", "\\[", "]", "\\]", "<", "‹", ">", "›").Replace(string(r))
}
func root(c conversation.Conversation) string {
	result := "**mark8ly support · " + text(c.CaseID) + "**\n" + "**Customer:** " + text(c.Customer.Name) + "\n**Email:** " + text(c.Customer.Email) + "\n**Business:** " + text(c.ClientTenantName) + "\n**Merchant ID:** " + text(c.ClientTenantID) + "\n**Case ID:** " + text(c.ID) + "\n**Opened (UTC):** " + c.CreatedAt.UTC().Format(time.RFC3339)
	if c.Intake != nil {
		result += "\n**Reason:** " + text(c.Intake.Reason) + "\n**Issue:** " + text(c.Intake.Status)
	}
	return result + "\n\nReply in this thread to answer the customer. Plain text only; replies follow the existing chat content policy."
}

// SyncConversation refuses unmarked or cross-product conversations.
func (w *Worker) SyncConversation(ctx context.Context, id string) error {
	var conv conversation.Conversation
	if err := w.db.Collection("conversations").FindOne(ctx, scope(id)).Decode(&conv); err != nil {
		return errors.New("support bridge: scoped case unavailable")
	}
	rid := rootID(id)
	if err := w.chat.Send(ctx, Message{ID: rid, RoomID: w.chat.cfg.RoomID, Text: root(conv)}); err != nil {
		return err
	}
	filter := bson.M{"conversation_id": id, "tenant_id": "platform", "store_id": "default", "rocketchat_delivered": bson.M{"$ne": true}, "source": bson.M{"$ne": "rocketchat"}}
	cur, err := w.db.Collection("messages").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(100))
	if err != nil {
		return errors.New("support bridge: message lookup")
	}
	var messages []message.Message
	err = cur.All(ctx, &messages)
	if err != nil {
		return errors.New("support bridge: decode messages")
	}
	for _, m := range messages {
		body := "**" + text(string(m.SenderType)) + " · " + text(m.SenderName) + "**\n" + text(m.Body)
		if err := w.chat.Send(ctx, Message{ID: "mark8ly-support-msg-" + m.ID, RoomID: w.chat.cfg.RoomID, ThreadID: rid, Text: body}); err != nil {
			return err
		}
		_, err := w.db.Collection("messages").UpdateOne(ctx, bson.M{"_id": m.ID, "conversation_id": id, "tenant_id": "platform", "store_id": "default"}, bson.M{"$set": bson.M{"rocketchat_delivered": true}})
		if err != nil {
			return errors.New("support bridge: delivery acknowledgement")
		}
	}
	// Closed cases still drain outgoing messages, but never accept another reply.
	if conv.Status == conversation.StatusClosed {
		return w.chat.Send(ctx, Message{ID: rid + "-closed", RoomID: w.chat.cfg.RoomID, ThreadID: rid, Text: "This support case is closed. New replies in this thread will not be delivered. Reopen it in the support inbox before replying."})
	}
	for offset := 0; offset < 10000; offset += 100 {
		replies, err := w.chat.Thread(ctx, rid, offset)
		if err != nil {
			return err
		}
		for _, reply := range replies {
			if !w.allowed(reply, rid) {
				continue
			}
			if err := w.receive(ctx, id, reply); err != nil {
				if errors.Is(err, errReplyRejected) {
					if err := w.chat.Send(ctx, Message{ID: "mark8ly-support-rejected-" + reply.ID, RoomID: w.chat.cfg.RoomID, ThreadID: rid, Text: "Reply was not delivered: the case is closed, assigned to another agent, or the text violates the existing chat policy. Check the support inbox before retrying."}); err != nil {
						return err
					}
					continue
				}
				return err
			}
		}
		if len(replies) < 100 {
			return nil
		}
	}
	return errors.New("support bridge: thread page limit exceeded")
}

func (w *Worker) allowed(m Message, thread string) bool {
	if m.RoomID != w.chat.cfg.RoomID || m.ThreadID != thread || m.ID == "" || m.User.ID == w.chat.cfg.UserID || m.Type != "" || m.EditedAt != nil || len(m.Attachments) > 0 || m.Timestamp.IsZero() || len(m.Text) > 16000 || strings.TrimSpace(m.Text) == "" {
		return false
	}
	for _, id := range w.chat.cfg.StaffIDs {
		if id != "" && m.User.ID == id {
			return true
		}
	}
	return false
}

var errReplyRejected = errors.New("support bridge: reply rejected")

func (w *Worker) receive(ctx context.Context, id string, reply Message) error {
	if !contentguard.Scan(reply.Text).Allowed {
		return errReplyRejected
	}
	session, err := w.db.Client().StartSession()
	if err != nil {
		return errors.New("support bridge: start transaction")
	}
	defer session.EndSession(ctx)
	actor := "rocketchat:" + reply.User.ID
	m := message.Message{ID: "rocketchat-" + reply.ID, ConversationID: id, TenantID: "platform", StoreID: "default", SenderType: message.SenderStaff, SenderID: actor, SenderName: reply.User.Name, Body: reply.Text, CreatedAt: reply.Timestamp}
	if m.SenderName == "" {
		m.SenderName = reply.User.Username
	}
	inserted := false
	_, err = session.WithTransaction(ctx, func(sc mongo.SessionContext) (any, error) {
		inserted = false
		existing, err := w.db.Collection("messages").CountDocuments(sc, bson.M{"_id": m.ID, "conversation_id": id, "tenant_id": "platform", "store_id": "default"})
		if err != nil {
			return nil, err
		}
		if existing > 0 {
			return nil, nil
		}
		var conv conversation.Conversation
		if err := w.db.Collection("conversations").FindOne(sc, scope(id)).Decode(&conv); err != nil {
			return nil, err
		}
		if conv.Status == conversation.StatusClosed || reply.Timestamp.Before(conv.CreatedAt) || (conv.Assignee != nil && conv.Assignee.UserID != actor) {
			return nil, errReplyRejected
		}
		data, err := bson.Marshal(m)
		if err != nil {
			return nil, err
		}
		var doc bson.M
		if err := bson.Unmarshal(data, &doc); err != nil {
			return nil, err
		}
		doc["source"] = "rocketchat"
		if _, err := w.db.Collection("messages").InsertOne(sc, doc); err != nil {
			return nil, err
		}
		update := bson.M{"$set": bson.M{"status": conversation.StatusActive, "assignee": conversation.Assignee{UserID: actor, Name: m.SenderName, AssignedAt: time.Now().UTC()}}, "$max": bson.M{"last_message_at": m.CreatedAt, "updated_at": m.CreatedAt}, "$inc": bson.M{"message_count": 1, "unread_count_customer": 1}}
		if _, err := w.db.Collection("conversations").UpdateOne(sc, scope(id), update); err != nil {
			return nil, err
		}
		if _, err := w.db.Collection("otto_audit").InsertOne(sc, conversation.AuditEvent{ID: m.ID, TenantID: "platform", StoreID: "default", ConversationID: id, CaseID: conv.CaseID, Action: "message.rocketchat_reply", Actor: conversation.Actor{Type: "staff", ID: actor, Name: m.SenderName}, At: m.CreatedAt, Meta: bson.M{"surface": "rocketchat", "room_id": w.chat.cfg.RoomID, "message_id": reply.ID}}); err != nil {
			return nil, err
		}
		inserted = true
		return nil, nil
	})
	if err != nil {
		if errors.Is(err, errReplyRejected) {
			return err
		}
		return errors.New("support bridge: reply transaction")
	}
	if inserted && w.hub != nil {
		e := hub.Envelope{Type: event.TypeMessageCreated, Payload: map[string]any{"message": m}}
		w.hub.Broadcast(hub.RoomConversation(id), e)
		w.hub.BroadcastInbox("platform", "default", hub.Envelope{Type: event.TypeConversationUpdated, Payload: map[string]any{"conversation_id": id, "last_message": m}})
	}
	return nil
}

// Run revisits cases fairly, retaining failures with backoff and a dead state.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := w.tick(ctx); err != nil && ctx.Err() == nil {
			w.log.Warn("support bridge: scan failed")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (w *Worker) tick(ctx context.Context) error {
	filter := bson.M{"product_id": "mark8ly", "tenant_id": "platform", "store_id": "default", "rocketchat_state": bson.M{"$ne": "dead"}, "$or": bson.A{bson.M{"rocketchat_next": bson.M{"$exists": false}}, bson.M{"rocketchat_next": bson.M{"$lte": time.Now().UTC()}}}}
	cur, err := w.db.Collection("conversations").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "rocketchat_next", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(20))
	if err != nil {
		return err
	}
	var cases []struct {
		ID       string              `bson:"_id"`
		Attempts int                 `bson:"rocketchat_attempts"`
		Status   conversation.Status `bson:"status"`
	}
	if err := cur.All(ctx, &cases); err != nil {
		return err
	}
	for _, c := range cases {
		jobCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := w.SyncConversation(jobCtx, c.ID)
		cancel()
		attempts := 0
		state := "active"
		delay := 5 * time.Second
		if c.Status == conversation.StatusClosed {
			delay = time.Hour
		}
		if err != nil {
			attempts = c.Attempts + 1
			delay = time.Duration(1<<min(attempts, 8))*time.Second + time.Duration(rand.IntN(1000))*time.Millisecond
			if attempts >= 12 {
				state = "dead"
			}
			w.log.Warn("support bridge: delivery deferred", "case_id", c.ID, "attempt", attempts, "state", state)
		}
		_, updateErr := w.db.Collection("conversations").UpdateOne(ctx, scope(c.ID), bson.M{"$set": bson.M{"rocketchat_attempts": attempts, "rocketchat_state": state, "rocketchat_next": time.Now().UTC().Add(delay)}})
		if updateErr != nil {
			return fmt.Errorf("support bridge: retry state: %w", updateErr)
		}
	}
	return nil
}
