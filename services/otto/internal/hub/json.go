package hub

import "encoding/json"

// jsonMarshal is a tiny indirection so tests can stub marshalling if needed.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// RoomConversation is the canonical key for a per-thread room.
func RoomConversation(conversationID string) string {
	return "conv:" + conversationID
}

// RoomInbox is the canonical key for the staff inbox room. New-conversation
// toasts broadcast here so every connected staff member for the given
// tenant+store sees incoming threads.
func RoomInbox(tenantID, storeID string) string {
	return "inbox:" + tenantID + ":" + storeID
}
