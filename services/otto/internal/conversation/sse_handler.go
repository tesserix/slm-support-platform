package conversation

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// sseHeartbeat keeps the stream alive through proxy/Cloudflare idle timeouts.
const sseHeartbeat = 25 * time.Second

// sse streams conversation events to the customer over Server-Sent Events —
// an alternative to the WebSocket for clients/networks where WS is blocked.
// Auth is identical to the WS endpoint: a short-lived signed ticket on the
// query string (the SSE path bypasses the Next.js proxy headers, same as WS,
// so it carries its own ticket). The same ws-ticket works for both
// transports — a ticket is bound to the conversation + audience, not the
// transport.
func (h *StorefrontHandler) sse(c *gin.Context) {
	tok, err := h.d.Tickets.Parse(c.Query("ticket"))
	if err != nil || tok.Audience != session.TicketAudienceCustomer {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	convID := c.Param("id")
	if tok.ConversationID != convID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ticket_conversation_mismatch"})
		return
	}
	conv, err := h.d.Conversations.GetForCustomer(c.Request.Context(), tok.TenantID, tok.StoreID, convID, tok.SessionToken)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}

	client := h.d.Hub.NewChannelClient(map[string]string{
		"role":            "customer",
		"conversation_id": conv.ID,
		"tenant_id":       conv.TenantID,
		"transport":       "sse",
	})
	h.d.Hub.Subscribe(client, hub.RoomConversation(conv.ID))
	defer h.d.Hub.Disconnect(client)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	// Defeat response buffering at proxies so frames flush immediately.
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	ch := client.Channel()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()

	c.Stream(func(w io.Writer) bool {
		select {
		case buf, ok := <-ch:
			if !ok {
				return false // hub closed the client (Disconnect)
			}
			// Unnamed event so the browser EventSource.onmessage fires; the
			// payload is the same Envelope JSON the WebSocket sends, so the
			// client parser is identical across transports.
			_, _ = fmt.Fprintf(w, "data: %s\n\n", buf)
			return true
		case <-heartbeat.C:
			_, _ = io.WriteString(w, ": ping\n\n")
			return true
		case <-c.Request.Context().Done():
			return false // peer went away
		}
	})
}
