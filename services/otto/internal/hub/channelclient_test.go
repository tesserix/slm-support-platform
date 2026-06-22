package hub

import (
	"encoding/json"
	"testing"
	"time"
)

// A channel (SSE) client must receive broadcasts on its rooms, just like a
// WebSocket client — the hub is transport-agnostic.
func TestChannelClient_ReceivesBroadcast(t *testing.T) {
	h := New(nil)
	c := h.NewChannelClient(map[string]string{"transport": "sse"})
	h.Subscribe(c, RoomConversation("c1"))

	h.Broadcast(RoomConversation("c1"), Envelope{Type: "otto.message.created", Payload: map[string]any{"body": "hi"}})

	select {
	case buf := <-c.Channel():
		var e Envelope
		if err := json.Unmarshal(buf, &e); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if e.Type != "otto.message.created" || e.Room != RoomConversation("c1") {
			t.Errorf("frame = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("channel client did not receive the broadcast")
	}
}

// Disconnecting a channel (SSE) client must not panic on the nil WebSocket
// conn and must close its channel so the SSE stream loop exits.
func TestChannelClient_DisconnectClosesChannelNoPanic(t *testing.T) {
	h := New(nil)
	c := h.NewChannelClient(nil)
	h.Subscribe(c, RoomConversation("c1"))
	h.Disconnect(c)
	if _, ok := <-c.Channel(); ok {
		t.Error("expected channel closed after Disconnect")
	}
}

// A broadcast to a room with no subscribers is a no-op (regression guard for
// the room-cleanup path).
func TestBroadcast_NoSubscribers(t *testing.T) {
	h := New(nil)
	h.Broadcast(RoomConversation("empty"), Envelope{Type: "t", Payload: map[string]any{}})
}
