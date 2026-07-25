// Package changestream watches Mongo for inserts/updates that
// originate OUTSIDE Otto (e.g. slm-router writing an assistant
// message + flipping conversation status to active) and rebroadcasts
// them onto Otto's WebSocket hub so the customer browser receives
// the change in real time.
//
// Without this, slm-router's writes land in Mongo but the widget
// only ever sees them on a manual refresh — which is the bug
// customers reported as "stuck on 'Connecting to support…'" even
// though the AI reply was already on disk.
package changestream

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/tesserix/slm-support-platform/services/otto/internal/event"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
)

// Broadcaster is the subset of *hub.Hub the watcher needs.
type Broadcaster interface {
	Broadcast(room string, env hub.Envelope)
	BroadcastInbox(tenantID, storeID string, env hub.Envelope)
}

// Watcher streams Mongo change events on `messages` (insert) and
// `conversations` (update) and forwards them onto the WebSocket hub.
type Watcher struct {
	DB     *mongo.Database
	Hub    Broadcaster
	Logger *slog.Logger
}

// Run blocks until ctx is done. On change-stream failure the loop
// reconnects with backoff so a transient Mongo blip doesn't take
// the watcher down for the rest of the process lifetime.
func (w *Watcher) Run(ctx context.Context) {
	if w.DB == nil || w.Hub == nil {
		w.log().Warn("changestream watcher disabled: missing DB or Hub")
		return
	}
	go w.watchMessages(ctx)
	go w.watchConversations(ctx)
	<-ctx.Done()
}

// ---------------------------------------------------------------------------
// messages — broadcast every newly-inserted message to the per-
// conversation room.
// ---------------------------------------------------------------------------
func (w *Watcher) watchMessages(ctx context.Context) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{"operationType": "insert"}}},
	}
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	w.loop(ctx, "messages", pipeline, opts, w.handleMessage)
}

func (w *Watcher) handleMessage(raw bson.Raw) {
	var ev struct {
		FullDocument struct {
			ID             string `bson:"_id"`
			ConversationID string `bson:"conversation_id"`
			TenantID       string `bson:"tenant_id"`
			StoreID        string `bson:"store_id"`
			SenderType     string `bson:"sender_type"`
			SenderID       string `bson:"sender_id"`
			SenderName     string `bson:"sender_name"`
			Body           string `bson:"body"`
			CreatedAt      time.Time `bson:"created_at"`
		} `bson:"fullDocument"`
	}
	if err := bson.Unmarshal(raw, &ev); err != nil {
		w.log().Warn("decode message change event", "err", err)
		return
	}
	d := ev.FullDocument
	if d.ConversationID == "" {
		return
	}
	payload, _ := json.Marshal(map[string]any{
		"message": map[string]any{
			"id":              d.ID,
			"conversation_id": d.ConversationID,
			"tenant_id":       d.TenantID,
			"store_id":        d.StoreID,
			"sender_type":     d.SenderType,
			"sender_id":       d.SenderID,
			"sender_name":     d.SenderName,
			"body":            d.Body,
			"created_at":      d.CreatedAt.Format(time.RFC3339Nano),
		},
	})
	var p map[string]any
	_ = json.Unmarshal(payload, &p)
	w.Hub.Broadcast(hub.RoomConversation(d.ConversationID), hub.Envelope{
		Type:    event.TypeMessageCreated,
		Payload: p,
	})
}

// ---------------------------------------------------------------------------
// conversations — broadcast updates (status flips, counter bumps,
// needs_human transitions) to the per-conversation room AND to the
// staff inbox so the inbox UI stays live.
// ---------------------------------------------------------------------------
func (w *Watcher) watchConversations(ctx context.Context) {
	pipeline := mongo.Pipeline{
		bson.D{{Key: "$match", Value: bson.M{
			"operationType": bson.M{"$in": bson.A{"insert", "update", "replace"}},
		}}},
	}
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	w.loop(ctx, "conversations", pipeline, opts, w.handleConversation)
}

func (w *Watcher) handleConversation(raw bson.Raw) {
	var ev struct {
		OperationType string `bson:"operationType"`
		FullDocument  bson.Raw `bson:"fullDocument"`
	}
	if err := bson.Unmarshal(raw, &ev); err != nil {
		w.log().Warn("decode conversation change event", "err", err)
		return
	}
	if len(ev.FullDocument) == 0 {
		return
	}
	var doc struct {
		ID       string `bson:"_id"`
		TenantID string `bson:"tenant_id"`
		StoreID  string `bson:"store_id"`
		Status   string `bson:"status"`
	}
	if err := bson.Unmarshal(ev.FullDocument, &doc); err != nil {
		w.log().Warn("decode conversation doc", "err", err)
		return
	}
	if doc.ID == "" {
		return
	}
	// Forward the FULL conversation document so the widget can
	// rehydrate its local state (status, counters, last messages, …)
	// from a single envelope.
	var full map[string]any
	_ = bson.Unmarshal(ev.FullDocument, &full)
	stringifyTimes(full)
	envType := event.TypeConversationUpdated
	if ev.OperationType == "insert" {
		envType = event.TypeConversationCreated
	}
	if doc.Status == "closed" {
		envType = event.TypeConversationClosed
	}
	envelope := hub.Envelope{
		Type:    envType,
		Payload: map[string]any{"conversation": full},
	}
	w.Hub.Broadcast(hub.RoomConversation(doc.ID), envelope)
	if doc.TenantID != "" && doc.StoreID != "" {
		w.Hub.BroadcastInbox(doc.TenantID, doc.StoreID, envelope)
	}
}

// stringifyTimes converts BSON DateTime values into RFC3339 strings
// recursively so the JSON the widget receives matches Otto's REST
// responses (which serialise via Go time.Time → ISO 8601).
func stringifyTimes(v any) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	for k, val := range m {
		switch t := val.(type) {
		case time.Time:
			m[k] = t.Format(time.RFC3339Nano)
		case map[string]any:
			stringifyTimes(t)
		case []any:
			for _, item := range t {
				stringifyTimes(item)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// loop — open a change stream and dispatch each event to handle until
// ctx ends. Reconnects on stream failure with capped backoff.
// ---------------------------------------------------------------------------
func (w *Watcher) loop(
	ctx context.Context,
	collection string,
	pipeline mongo.Pipeline,
	opts *options.ChangeStreamOptions,
	handle func(bson.Raw),
) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := w.DB.Collection(collection).Watch(ctx, pipeline, opts)
		if err != nil {
			w.log().Warn("open change stream failed", "collection", collection, "err", err)
			if !w.sleep(ctx, backoff) {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		w.log().Info("change stream open", "collection", collection)
		for stream.Next(ctx) {
			handle(stream.Current)
		}
		if err := stream.Err(); err != nil && ctx.Err() == nil {
			w.log().Warn("change stream error, will reconnect", "collection", collection, "err", err)
		}
		_ = stream.Close(ctx)
		if !w.sleep(ctx, backoff) {
			return
		}
	}
}

func (w *Watcher) sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (w *Watcher) log() *slog.Logger {
	if w.Logger == nil {
		return slog.Default()
	}
	return w.Logger
}
