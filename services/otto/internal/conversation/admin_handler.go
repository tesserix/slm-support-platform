package conversation

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"go.mongodb.org/mongo-driver/bson"

	"github.com/tesserix/slm-support-platform/services/otto/internal/auth"
	"github.com/tesserix/slm-support-platform/services/otto/internal/event"
	"github.com/tesserix/slm-support-platform/services/otto/internal/hub"
	"github.com/tesserix/slm-support-platform/services/otto/internal/message"
	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// AdminDeps is the dependency bag for the staff-side handler.
type AdminDeps struct {
	Conversations *Repository
	Availability  *AvailabilityRepository
	Audit         *AuditRepository
	Messages      *message.Repository
	Hub           *hub.Hub
	Tickets       *session.TicketSigner
	Logger        *slog.Logger
}

// emitAudit fires an audit event and swallows errors — the primary
// action must never fail because the audit write did. We log the
// failure so an ops/SRE can detect systematic drops.
func (h *AdminHandler) emitAudit(c *gin.Context, conv *Conversation, action AuditAction, meta map[string]any) {
	if h.d.Audit == nil || conv == nil {
		return
	}
	ev := AuditEvent{
		TenantID:       conv.TenantID,
		StoreID:        conv.StoreID,
		ConversationID: conv.ID,
		CaseID:         conv.CaseID,
		Action:         action,
		Actor: Actor{
			Type:  "staff",
			ID:    c.GetString(auth.CtxUserID),
			Name:  c.GetString(auth.CtxUserName),
			Email: c.GetString(auth.CtxUserEmail),
		},
	}
	if meta != nil {
		ev.Meta = bson.M(meta)
	}
	if err := h.d.Audit.Emit(c.Request.Context(), ev); err != nil {
		h.d.Logger.Warn("otto: audit emit failed", "err", err, "action", action)
	}
}

// AdminHandler exposes the /api/v1/admin/otto/* endpoints.
type AdminHandler struct{ d AdminDeps }

func NewAdminHandler(d AdminDeps) *AdminHandler { return &AdminHandler{d: d} }

// Register mounts the REST routes. The caller must apply auth.StaffAuth
// and auth.StoreResolver so every route has tenant_id + store_id set.
//
// WebSocket endpoints live in RegisterWS, which must be mounted on a
// different group that DOES NOT run StaffAuth — the WS path is routed to
// Otto directly by Istio, bypassing the admin Next.js proxy that normally
// injects identity headers. Ticket auth handles that case instead.
func (h *AdminHandler) Register(r *gin.RouterGroup) {
	r.GET("/conversations", h.list)
	r.GET("/conversations/:id", h.get)
	r.GET("/conversations/:id/messages", h.listMessages)
	r.POST("/conversations/:id/accept", h.accept)
	r.POST("/conversations/:id/messages", h.postMessage)
	r.POST("/conversations/:id/close", h.close)
	r.POST("/conversations/:id/reopen", h.reopen)
	r.POST("/ws-ticket", h.inboxWSTicket)
	r.POST("/conversations/:id/ws-ticket", h.conversationWSTicket)

	// Phase 2 — staff availability pool.
	r.GET("/me/availability", h.getAvailability)
	r.POST("/me/availability", h.setAvailability)
	r.POST("/accept-next", h.acceptNext)

	// Audit trail — per-case timeline + store-wide tail.
	r.GET("/conversations/:id/audit", h.listAudit)
	r.GET("/audit", h.listAuditRecent)
}

// listAudit returns the timeline for a single case. Staff only; the
// loadForStaff check restricts to the caller's store scope.
func (h *AdminHandler) listAudit(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	if h.d.Audit == nil {
		c.JSON(http.StatusOK, gin.H{"events": []AuditEvent{}})
		return
	}
	events, err := h.d.Audit.ListByConversation(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, 500)
	if err != nil {
		h.d.Logger.Error("otto: list audit", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "audit_list_failed"})
		return
	}
	if events == nil {
		events = []AuditEvent{}
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// listAuditRecent returns the latest N events across the store, for a
// future "support audit log" admin page. Scoped to tenant+store.
func (h *AdminHandler) listAuditRecent(c *gin.Context) {
	if h.d.Audit == nil {
		c.JSON(http.StatusOK, gin.H{"events": []AuditEvent{}})
		return
	}
	events, err := h.d.Audit.ListRecent(
		c.Request.Context(),
		c.GetString(auth.CtxTenantID),
		c.GetString(auth.CtxStoreID),
		100,
	)
	if err != nil {
		h.d.Logger.Error("otto: list audit recent", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "audit_list_failed"})
		return
	}
	if events == nil {
		events = []AuditEvent{}
	}
	c.JSON(http.StatusOK, gin.H{"events": events})
}

// RegisterWS mounts the actual WebSocket endpoints. Unlike Register this
// group must NOT run StaffAuth — ticket auth is used instead.
func (h *AdminHandler) RegisterWS(r *gin.RouterGroup) {
	r.GET("/ws", h.inboxWebsocket)
	r.GET("/conversations/:id/ws", h.conversationWebsocket)
}

func (h *AdminHandler) list(c *gin.Context) {
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)
	userID := c.GetString(auth.CtxUserID)

	p := ListInboxParams{TenantID: tenantID, StoreID: storeID}
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
		p.AssigneeUserID = userID
	case "unassigned":
		p.OnlyUnassigned = true
	}

	items, err := h.d.Conversations.ListInbox(c.Request.Context(), p)
	if err != nil {
		h.d.Logger.Error("otto: list inbox", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	if items == nil {
		items = []Conversation{}
	}
	c.JSON(http.StatusOK, gin.H{"conversations": items})
}

func (h *AdminHandler) get(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	// Staff opening the thread clears their side of the unread counter.
	_ = h.d.Conversations.ClearUnread(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, AudienceStaff)
	c.JSON(http.StatusOK, gin.H{"conversation": conv})
}

func (h *AdminHandler) listMessages(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	msgs, err := h.d.Messages.ListByConversation(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, 500)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	if msgs == nil {
		msgs = []message.Message{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

func (h *AdminHandler) accept(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	userID := c.GetString(auth.CtxUserID)
	name := c.GetString(auth.CtxUserName)
	email := c.GetString(auth.CtxUserEmail)

	updated, err := h.d.Conversations.Accept(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, Assignee{
		UserID: userID,
		Name:   name,
		Email:  email,
	})
	if err != nil {
		h.d.Logger.Error("otto: accept", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "accept_failed"})
		return
	}

	// Post a system message so both sides get a visible "Sara joined the
	// chat" in the thread — better UX than a silent state flip.
	if updated.Status == StatusActive && updated.Assignee != nil && updated.Assignee.UserID == userID {
		now := time.Now().UTC()
		sys := &message.Message{
			ID:             uuid.NewString(),
			ConversationID: updated.ID,
			TenantID:       updated.TenantID,
			StoreID:        updated.StoreID,
			SenderType:     message.SenderSystem,
			SenderName:     "Otto",
			Body:           systemJoinBody(updated.Assignee.Name, updated.Assignee.Email),
			CreatedAt:      now,
		}
		_ = h.d.Messages.Insert(c.Request.Context(), sys)
		_ = h.d.Conversations.BumpOnMessage(c.Request.Context(), updated.TenantID, updated.StoreID, updated.ID, AudienceCustomer, now)
		h.d.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
			Type:    event.TypeMessageCreated,
			Payload: map[string]any{"message": sys},
		})
	}

	h.d.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.d.Hub.Broadcast(hub.RoomInbox(updated.TenantID, updated.StoreID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.emitAudit(c, updated, AuditCaseAccepted, map[string]any{
		"path": "manual",
	})
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

type adminPostMessageRequest struct {
	Body string `json:"body"`
}

func (h *AdminHandler) postMessage(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	if conv.Status == StatusClosed {
		c.JSON(http.StatusConflict, gin.H{"error": "thread_closed"})
		return
	}
	// Only the assignee (or an unassigned thread, for the "pick up and
	// reply" flow where accept is implicit) can post. Anything stricter
	// can be added later behind an FGA check.
	userID := c.GetString(auth.CtxUserID)
	if conv.Assignee != nil && conv.Assignee.UserID != userID {
		c.JSON(http.StatusForbidden, gin.H{"error": "not_assignee"})
		return
	}

	var body adminPostMessageRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty_message"})
		return
	}

	now := time.Now().UTC()
	msg := &message.Message{
		ID:             uuid.NewString(),
		ConversationID: conv.ID,
		TenantID:       conv.TenantID,
		StoreID:        conv.StoreID,
		SenderType:     message.SenderStaff,
		SenderID:       userID,
		SenderName:     displayName(c.GetString(auth.CtxUserName), c.GetString(auth.CtxUserEmail), "Support"),
		Body:           body.Body,
		CreatedAt:      now,
	}
	if err := h.d.Messages.Insert(c.Request.Context(), msg); err != nil {
		h.d.Logger.Error("otto: insert staff msg", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}
	if err := h.d.Conversations.BumpOnMessage(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, AudienceCustomer, now); err != nil {
		h.d.Logger.Warn("otto: bump conversation", "err", err)
	}

	h.d.Hub.Broadcast(hub.RoomConversation(conv.ID), hub.Envelope{
		Type:    event.TypeMessageCreated,
		Payload: map[string]any{"message": msg},
	})
	h.d.Hub.Broadcast(hub.RoomInbox(conv.TenantID, conv.StoreID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation_id": conv.ID, "last_message": msg},
	})
	c.JSON(http.StatusCreated, gin.H{"message": msg})
}

func (h *AdminHandler) reopen(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	updated, err := h.d.Conversations.Reopen(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "reopen_failed"})
		return
	}
	h.d.Hub.Broadcast(hub.RoomConversation(conv.ID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.d.Hub.Broadcast(hub.RoomInbox(conv.TenantID, conv.StoreID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.emitAudit(c, updated, AuditCaseReopened, nil)
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

func (h *AdminHandler) close(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	updated, err := h.d.Conversations.Close(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "close_failed"})
		return
	}
	// Free the staff back into the pool — they stay available but
	// their current_case_id is cleared so the next Accept Next click
	// can land them on a new customer.
	if h.d.Availability != nil && updated.Assignee != nil {
		if releaseErr := h.d.Availability.ReleaseCase(c.Request.Context(), updated.TenantID, updated.StoreID, updated.Assignee.UserID); releaseErr != nil {
			h.d.Logger.Warn("otto: release availability on close", "err", releaseErr)
		}
	}
	h.d.Hub.Broadcast(hub.RoomConversation(conv.ID), hub.Envelope{
		Type:    event.TypeConversationClosed,
		Payload: map[string]any{"conversation": updated},
	})
	h.d.Hub.Broadcast(hub.RoomInbox(conv.TenantID, conv.StoreID), hub.Envelope{
		Type:    event.TypeConversationClosed,
		Payload: map[string]any{"conversation": updated},
	})
	h.emitAudit(c, updated, AuditCaseClosed, nil)
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

// ── Availability pool (phase 2) ─────────────────────────────────────

// getAvailability returns the current pool state for the caller. If
// the staff has never toggled, we return a zero-value row so the UI
// can render the "Paused" default without needing a separate code
// path.
func (h *AdminHandler) getAvailability(c *gin.Context) {
	if h.d.Availability == nil {
		c.JSON(http.StatusOK, gin.H{"available": false})
		return
	}
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)
	userID := c.GetString(auth.CtxUserID)
	row, err := h.d.Availability.Get(c.Request.Context(), tenantID, storeID, userID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusOK, gin.H{"available": false})
			return
		}
		h.d.Logger.Error("otto: get availability", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "availability_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"availability": row})
}

type setAvailabilityRequest struct {
	Available bool `json:"available"`
}

func (h *AdminHandler) setAvailability(c *gin.Context) {
	if h.d.Availability == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "availability_not_enabled"})
		return
	}
	var body setAvailabilityRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	row, err := h.d.Availability.SetAvailable(
		c.Request.Context(),
		c.GetString(auth.CtxTenantID),
		c.GetString(auth.CtxStoreID),
		c.GetString(auth.CtxUserID),
		c.GetString(auth.CtxUserName),
		c.GetString(auth.CtxUserEmail),
		body.Available,
	)
	if err != nil {
		h.d.Logger.Error("otto: set availability", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "availability_write_failed"})
		return
	}
	// Availability toggles are not tied to a specific case, but we
	// still want the trail for staff-behaviour audits. conversation_id
	// is empty on these rows; the timeline UI filters them out per
	// case, and the "recent" tail shows them alongside case actions.
	if h.d.Audit != nil {
		action := AuditStaffAvailable
		if !body.Available {
			action = AuditStaffPaused
		}
		if err := h.d.Audit.Emit(c.Request.Context(), AuditEvent{
			TenantID: c.GetString(auth.CtxTenantID),
			StoreID:  c.GetString(auth.CtxStoreID),
			Action:   action,
			Actor: Actor{
				Type:  "staff",
				ID:    c.GetString(auth.CtxUserID),
				Name:  c.GetString(auth.CtxUserName),
				Email: c.GetString(auth.CtxUserEmail),
			},
		}); err != nil {
			h.d.Logger.Warn("otto: audit availability", "err", err)
		}
	}
	c.JSON(http.StatusOK, gin.H{"availability": row})
}

// acceptNext pops the oldest pending conversation (FIFO) and assigns
// it to the caller. Gates:
//   - staff must be marked available
//   - staff must have no case in flight (CurrentCaseID empty)
//   - a pending case must exist
// We reserve the slot on the staff row first, then do the
// findOneAndUpdate on the conversation. If the second step fails we
// roll back the staff row so we never lock a staff to a non-existent
// case.
func (h *AdminHandler) acceptNext(c *gin.Context) {
	if h.d.Availability == nil {
		c.JSON(http.StatusNotImplemented, gin.H{"error": "availability_not_enabled"})
		return
	}
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)
	userID := c.GetString(auth.CtxUserID)
	userName := c.GetString(auth.CtxUserName)
	userEmail := c.GetString(auth.CtxUserEmail)

	// Two-phase claim: pop the case first, then record it on the
	// staff row. Reversing the order risks locking a staff slot with
	// no case to show for it. If the staff-row update fails we free
	// the case by reopening it — the case is back in the pool on the
	// next click.
	updated, err := h.d.Conversations.PopNextPending(c.Request.Context(), tenantID, storeID, Assignee{
		UserID: userID,
		Name:   userName,
		Email:  userEmail,
	})
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusOK, gin.H{"conversation": nil})
			return
		}
		h.d.Logger.Error("otto: accept-next pop", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "accept_failed"})
		return
	}
	if err := h.d.Availability.ClaimCase(c.Request.Context(), tenantID, storeID, userID, updated.ID); err != nil {
		// Either not available or already on a case — roll the case
		// back to pending so the next click can pick it up.
		if _, reopenErr := h.d.Conversations.Reopen(c.Request.Context(), tenantID, storeID, updated.ID); reopenErr != nil {
			h.d.Logger.Error("otto: accept-next rollback", "err", reopenErr)
		}
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusConflict, gin.H{
				"error":   "not_available",
				"message": "You need to be available and have no case in progress to accept the next customer.",
			})
			return
		}
		h.d.Logger.Error("otto: accept-next claim", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "accept_failed"})
		return
	}

	// Reuse the "someone joined" system message the manual accept
	// path posts — better UX than a silent state flip.
	if updated.Assignee != nil {
		now := time.Now().UTC()
		sys := &message.Message{
			ID:             uuid.NewString(),
			ConversationID: updated.ID,
			TenantID:       updated.TenantID,
			StoreID:        updated.StoreID,
			SenderType:     message.SenderSystem,
			SenderName:     "Otto",
			Body:           systemJoinBody(updated.Assignee.Name, updated.Assignee.Email),
			CreatedAt:      now,
		}
		_ = h.d.Messages.Insert(c.Request.Context(), sys)
		_ = h.d.Conversations.BumpOnMessage(c.Request.Context(), updated.TenantID, updated.StoreID, updated.ID, AudienceCustomer, now)
		h.d.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
			Type:    event.TypeMessageCreated,
			Payload: map[string]any{"message": sys},
		})
	}
	h.d.Hub.Broadcast(hub.RoomConversation(updated.ID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.d.Hub.Broadcast(hub.RoomInbox(updated.TenantID, updated.StoreID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.emitAudit(c, updated, AuditCaseAcceptedNext, map[string]any{
		"path": "fifo",
	})
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

// inboxWSTicket mints a ticket scoped to the staff's inbox (tenant+store
// but no specific conversation).
func (h *AdminHandler) inboxWSTicket(c *gin.Context) {
	raw, _, err := h.d.Tickets.Issue(session.Ticket{
		Audience:  session.TicketAudienceStaff,
		TenantID:  c.GetString(auth.CtxTenantID),
		StoreID:   c.GetString(auth.CtxStoreID),
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

// conversationWSTicket mints a ticket scoped to a specific thread.
func (h *AdminHandler) conversationWSTicket(c *gin.Context) {
	conv, ok := h.loadForStaff(c)
	if !ok {
		return
	}
	raw, _, err := h.d.Tickets.Issue(session.Ticket{
		Audience:       session.TicketAudienceStaff,
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

// inboxWebsocket accepts a staff-audience ticket (no conversation_id) and
// subscribes to the tenant+store inbox room.
func (h *AdminHandler) inboxWebsocket(c *gin.Context) {
	tok, err := h.d.Tickets.Parse(c.Query("ticket"))
	if err != nil || tok.Audience != session.TicketAudienceStaff {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	conn, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := h.d.Hub.NewClient(conn, map[string]string{
		"role":      "staff",
		"user_id":   tok.UserID,
		"tenant_id": tok.TenantID,
		"store_id":  tok.StoreID,
	})
	h.d.Hub.Subscribe(client, hub.RoomInbox(tok.TenantID, tok.StoreID))
	client.Run(h.d.Hub)
}

// conversationWebsocket accepts a staff-audience ticket bound to a
// specific conversation and subscribes the client to both the thread room
// and the inbox room (so the agent keeps getting new-thread pings while
// they're focused on one).
func (h *AdminHandler) conversationWebsocket(c *gin.Context) {
	tok, err := h.d.Tickets.Parse(c.Query("ticket"))
	if err != nil || tok.Audience != session.TicketAudienceStaff {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	convID := c.Param("id")
	if tok.ConversationID != convID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ticket_conversation_mismatch"})
		return
	}
	// Scope check — ticket could be forged for any conversation id, so
	// verify the row actually exists within the ticket's tenant+store.
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
		"role":            "staff",
		"user_id":         tok.UserID,
		"conversation_id": conv.ID,
		"tenant_id":       conv.TenantID,
		"store_id":        conv.StoreID,
	})
	h.d.Hub.Subscribe(client, hub.RoomConversation(conv.ID))
	h.d.Hub.Subscribe(client, hub.RoomInbox(conv.TenantID, conv.StoreID))
	client.Run(h.d.Hub)
}

// loadForStaff loads a conversation and confirms it belongs to the staff
// caller's tenant+store. Returns false after writing an error response.
func (h *AdminHandler) loadForStaff(c *gin.Context) (*Conversation, bool) {
	id := c.Param("id")
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)
	conv, err := h.d.Conversations.GetByID(c.Request.Context(), tenantID, storeID, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil, false
		}
		h.d.Logger.Error("otto: load conversation (admin)", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup_failed"})
		return nil, false
	}
	return conv, true
}

func systemJoinBody(name, email string) string {
	who := name
	if who == "" {
		who = email
	}
	if who == "" {
		who = "A support agent"
	}
	return who + " joined the conversation."
}

// Re-export the upgrader so we share one instance across handlers without
// circular imports.
var _ = websocket.Upgrader{}
