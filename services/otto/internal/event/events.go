package event

// Wire event type names used on the WebSocket and (later) pub/sub. Keep
// these as constants so the React widget and the admin inbox parse a stable
// contract.
const (
	TypeConversationCreated = "otto.conversation.created"
	TypeConversationUpdated = "otto.conversation.updated"
	TypeConversationClosed  = "otto.conversation.closed"
	TypeMessageCreated      = "otto.message.created"
)
