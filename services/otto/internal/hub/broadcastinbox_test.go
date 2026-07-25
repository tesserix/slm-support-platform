package hub

import (
	"testing"
	"time"
)

// drain reads one frame with a timeout so a missing broadcast fails the
// test instead of hanging it.
func drain(t *testing.T, c *Client) []byte {
	t.Helper()
	select {
	case buf := <-c.Channel():
		return buf
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for frame")
		return nil
	}
}

func TestBroadcastInboxReachesTenantAndPlatformRooms(t *testing.T) {
	h := New(nil)

	tenantClient := h.NewChannelClient(map[string]string{"role": "staff"})
	h.Subscribe(tenantClient, RoomInbox("homechef", "default"))

	platformClient := h.NewChannelClient(map[string]string{"role": "platform"})
	h.Subscribe(platformClient, RoomPlatformInbox())

	otherTenantClient := h.NewChannelClient(map[string]string{"role": "staff"})
	h.Subscribe(otherTenantClient, RoomInbox("fanzone", "default"))

	h.BroadcastInbox("homechef", "default", Envelope{
		Type:    "conversation.updated",
		Payload: map[string]any{"conversation_id": "c1"},
	})

	if buf := drain(t, tenantClient); len(buf) == 0 {
		t.Fatal("tenant staff client got empty frame")
	}
	if buf := drain(t, platformClient); len(buf) == 0 {
		t.Fatal("platform client got empty frame")
	}
	select {
	case <-otherTenantClient.Channel():
		t.Fatal("other tenant's inbox must not receive the frame")
	case <-time.After(50 * time.Millisecond):
		// correct — isolation preserved
	}
}

// The platform room key must never collide with a real tenant's inbox
// room — a tenant literally named "platform" would otherwise leak.
func TestPlatformRoomKeyCannotCollideWithTenantRooms(t *testing.T) {
	if RoomPlatformInbox() == RoomInbox("platform", "default") {
		t.Fatal("platform room collides with tenant room")
	}
}
