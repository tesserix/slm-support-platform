package conversation

import (
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/tesserix/slm-support-platform/services/otto/internal/auth"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// PlatformHandler exposes the cross-tenant /api/v1/platform/otto inbox
// for Tesserix platform admins (tesserix-home). It owns only the parts
// that differ from the tenant-scoped admin surface — cross-tenant
// lookup, the platform ticket audience, and the platform inbox room.
// Per-conversation actions load the row WITHOUT tenant scope, inject
// the row's real tenant+store into the gin context, then delegate to
// the AdminHandler methods so accept/reply/close behavior (system join
// message, assignee guard, contentguard, audit, broadcasts) stays
// single-sourced.
type PlatformHandler struct {
	admin *AdminHandler
	d     AdminDeps
}

func NewPlatformHandler(admin *AdminHandler, d AdminDeps) *PlatformHandler {
	return &PlatformHandler{admin: admin, d: d}
}

// Register mounts the REST routes. The caller must apply
// auth.PlatformStaff — every route here assumes an attributed staff
// identity and the internal-auth secret have already been enforced.
func (h *PlatformHandler) Register(r *gin.RouterGroup) {
	r.GET("/conversations", h.list)
	r.GET("/conversations/:id", h.withScope(h.admin.get))
	r.GET("/conversations/:id/messages", h.withScope(h.admin.listMessages))
	r.POST("/conversations/:id/accept", h.withScope(h.admin.accept))
	r.POST("/conversations/:id/messages", h.withScope(h.admin.postMessage))
	r.POST("/conversations/:id/close", h.withScope(h.admin.close))
	r.POST("/ws-ticket", h.inboxWSTicket)
	r.POST("/conversations/:id/ws-ticket", h.conversationWSTicket)
}

// RegisterWS mounts the WebSocket endpoints. Must be a group WITHOUT
// PlatformStaff — Istio routes WS straight to otto, so ticket auth
// replaces header auth (same pattern as AdminHandler.RegisterWS).
func (h *PlatformHandler) RegisterWS(r *gin.RouterGroup) {
	r.GET("/ws", h.inboxWebsocket)
	r.GET("/conversations/:id/ws", h.conversationWebsocket)
}

// withScope loads the conversation cross-tenant, pins its real
// tenant+store into the context, then runs the tenant-scoped admin
// handler. Every downstream repo write therefore stays scoped to the
// row's own tenant — platform access never widens a write.
func (h *PlatformHandler) withScope(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		conv, ok := h.loadAnyTenant(c)
		if !ok {
			return
		}
		c.Set(auth.CtxTenantID, conv.TenantID)
		c.Set(auth.CtxStoreID, conv.StoreID)
		next(c)
	}
}

func (h *PlatformHandler) loadAnyTenant(c *gin.Context) (*Conversation, bool) {
	conv, err := h.d.Conversations.GetByIDAnyTenant(c.Request.Context(), c.Param("id"))
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil, false
		}
		h.d.Logger.Error("otto: load conversation (platform)", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup_failed"})
		return nil, false
	}
	return conv, true
}

func (h *PlatformHandler) list(c *gin.Context) {
	p := PlatformListParams{TenantID: strings.TrimSpace(c.Query("tenant"))}
	switch strings.ToLower(c.Query("status")) {
	case "pending":
		p.Status = StatusPending
	case "active":
		p.Status = StatusActive
	case "closed":
		p.Status = StatusClosed
	}
	switch strings.ToLower(c.Query("assignee")) {
	case "mine":
		p.AssigneeUserID = c.GetString(auth.CtxUserID)
	case "unassigned":
		p.OnlyUnassigned = true
	}
	items, err := h.d.Conversations.ListPlatformInbox(c.Request.Context(), p)
	if err != nil {
		h.d.Logger.Error("otto: list platform inbox", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	if items == nil {
		items = []Conversation{}
	}
	c.JSON(http.StatusOK, gin.H{"conversations": items})
}

// inboxWSTicket mints a cross-tenant inbox ticket. The sentinel "*"
// scope marks it as platform-wide; the WS handler checks audience, not
// scope, so the sentinel never reaches a Mongo filter.
func (h *PlatformHandler) inboxWSTicket(c *gin.Context) {
	raw, _, err := h.d.Tickets.Issue(session.Ticket{
		Audience:  session.TicketAudiencePlatform,
		TenantID:  "*",
		StoreID:   "*",
		UserID:    c.GetString(auth.CtxUserID),
		UserName:  c.GetString(auth.CtxUserName),
		UserEmail: c.GetString(auth.CtxUserEmail),
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ticket_mint_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": raw})
}

// conversationWSTicket mints a platform ticket bound to one thread,
// carrying the row's REAL tenant+store so the WS handler can re-verify
// the thread still exists in that scope at connect time.
func (h *PlatformHandler) conversationWSTicket(c *gin.Context) {
	conv, ok := h.loadAnyTenant(c)
	if !ok {
		return
	}
	raw, _, err := h.d.Tickets.Issue(session.Ticket{
		Audience:       session.TicketAudiencePlatform,
		TenantID:       conv.TenantID,
		StoreID:        conv.StoreID,
		UserID:         c.GetString(auth.CtxUserID),
		UserName:       c.GetString(auth.CtxUserName),
		UserEmail:      c.GetString(auth.CtxUserEmail),
		ConversationID: conv.ID,
	})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ticket_mint_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": raw})
}

// inboxWebsocket subscribes a platform admin to the cross-tenant inbox
// room. Platform audience ONLY — a tenant staff ticket is rejected
// here exactly as a platform ticket is rejected on the tenant inbox WS.
func (h *PlatformHandler) inboxWebsocket(c *gin.Context) {
	tok, err := h.d.Tickets.Parse(c.Query("ticket"))
	if err != nil || tok.Audience != session.TicketAudiencePlatform {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	conn, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := h.d.Hub.NewClient(conn, map[string]string{
		"role":    "platform",
		"user_id": tok.UserID,
	})
	h.d.Hub.Subscribe(client, hub.RoomPlatformInbox())
	client.Run(h.d.Hub)
}

// conversationWebsocket joins one thread's room (plus the platform
// inbox room so new-thread pings keep arriving while focused).
func (h *PlatformHandler) conversationWebsocket(c *gin.Context) {
	tok, err := h.d.Tickets.Parse(c.Query("ticket"))
	if err != nil || tok.Audience != session.TicketAudiencePlatform {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	convID := c.Param("id")
	if tok.ConversationID != convID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ticket_conversation_mismatch"})
		return
	}
	// The ticket carries the scope stamped at mint time; confirm the
	// thread still exists in exactly that scope.
	conv, err := h.d.Conversations.GetByID(c.Request.Context(), tok.TenantID, tok.StoreID, convID)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	conn, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := h.d.Hub.NewClient(conn, map[string]string{
		"role":            "platform",
		"user_id":         tok.UserID,
		"conversation_id": conv.ID,
	})
	h.d.Hub.Subscribe(client, hub.RoomConversation(conv.ID))
	h.d.Hub.Subscribe(client, hub.RoomPlatformInbox())
	client.Run(h.d.Hub)
}
