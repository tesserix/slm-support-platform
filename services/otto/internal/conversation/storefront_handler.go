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
	"github.com/tesserix/slm-support-platform/services/otto/internal/otp"
	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// StorefrontDeps is the dependency bag for the customer-side handler.
type StorefrontDeps struct {
	Conversations *Repository
	// Availability is optional — when present, it's consulted to
	// annotate queue status with an "all agents busy" flag and to
	// release staff when the customer closes the case themselves.
	Availability *AvailabilityRepository
	Audit        *AuditRepository
	Messages     *message.Repository
	Hub          *hub.Hub
	Signer       *session.Signer
	Tickets      *session.TicketSigner
	OTP          *otp.Service
	CookieName   string
	CookieDomain string
	CookieSecure bool
	Logger       *slog.Logger
}

// emitCustomerAudit is the customer-side equivalent of the admin
// emitter — Actor.Type is "customer" and ID/Name/Email come from the
// conversation's Customer record, not the gin context.
func (h *StorefrontHandler) emitCustomerAudit(c *gin.Context, conv *Conversation, action AuditAction, meta map[string]any) {
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
			Type:  "customer",
			ID:    conv.Customer.UserID,
			Name:  conv.Customer.Name,
			Email: conv.Customer.Email,
		},
	}
	if meta != nil {
		ev.Meta = bson.M(meta)
	}
	if err := h.d.Audit.Emit(c.Request.Context(), ev); err != nil {
		h.d.Logger.Warn("otto: audit emit failed (customer)", "err", err, "action", action)
	}
}

// StorefrontHandler exposes the /api/v1/storefront/otto/* endpoints.
type StorefrontHandler struct{ d StorefrontDeps }

func NewStorefrontHandler(d StorefrontDeps) *StorefrontHandler {
	return &StorefrontHandler{d: d}
}

// Register mounts routes onto the given group. The group MUST already have
// auth.CustomerContext applied; any route requiring an existing session also
// requires auth.RequireCustomerSession (applied per-route here so the
// create endpoint can issue the cookie on the first call).
func (h *StorefrontHandler) Register(r *gin.RouterGroup) {
	r.POST("/conversations", h.create)

	// Resume — the widget calls this on every mount. If the browser has a
	// valid otto_session cookie we return the open thread that session
	// owns along with its messages, so the customer never loses context
	// after a refresh. Runs without RequireCustomerSession so the endpoint
	// soft-fails (returns conversation:null) when there's no cookie, no
	// open thread, or the cookie is for a different store.
	r.GET("/resume", h.resume)

	// These require a valid otto_session cookie already.
	withSession := r.Group("")
	withSession.Use(auth.RequireCustomerSession(h.d.CookieName, h.d.Signer))
	withSession.GET("/conversations/:id", h.get)
	withSession.GET("/conversations/:id/messages", h.listMessages)
	withSession.POST("/conversations/:id/messages", h.postMessage)
	withSession.POST("/conversations/:id/close", h.close)
	// Queue position for the "connecting…" screen. Cheap read — the
	// widget polls this every few seconds until status flips to active.
	withSession.GET("/conversations/:id/queue", h.queue)
	// Post-case survey. Only usable once the thread is closed.
	withSession.POST("/conversations/:id/feedback", h.feedback)
	// Issuing a WebSocket ticket requires the caller to already own the
	// conversation (standard cookie auth through the Next.js proxy). The
	// ticket itself then travels via ?ticket= on the WS upgrade, which
	// bypasses the proxy.
	withSession.POST("/conversations/:id/ws-ticket", h.wsTicket)
}

// RegisterWS mounts the actual WebSocket upgrade endpoint on a group that
// MUST NOT run CustomerContext — Istio routes /api/v1/.../ws to Otto
// directly, so the auth headers the Next.js proxy would otherwise inject
// never arrive. Ticket auth takes over; see wsTicket above.
func (h *StorefrontHandler) RegisterWS(r *gin.RouterGroup) {
	r.GET("/conversations/:id/ws", h.websocket)
}

type createRequest struct {
	Subject string `json:"subject"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Message string `json:"message"`
	// OTPCode is required for anonymous callers. When the storefront
	// proxy forwards a logged-in user's identity via X-User-Id /
	// X-User-Email headers the OTP step is skipped entirely.
	OTPCode string `json:"otp_code"`
	// Intake form — the widget collects these BEFORE the first message
	// so staff opens the case with context already populated.
	Reason string `json:"reason"`
	// StatusInfo is the free-text "what's going on" field. Named with
	// an `_info` suffix on the wire so it doesn't collide with the
	// conversation lifecycle Status enum.
	StatusInfo string `json:"status_info"`
	DOB        string `json:"dob"`
}

func (h *StorefrontHandler) create(c *gin.Context) {
	// Tenant resolution order: the JWT-derived tenant takes priority
	// (it's set by the auth middleware from a signed token and can't
	// be spoofed), with the X-Tenant-ID header from the widget as a
	// fallback for storefronts that don't yet authenticate on the
	// intake call. The header is the standard widget-side contract —
	// see @tesserix/otto-widget which sends it on every Otto call.
	tenantID := c.GetString(auth.CtxTenantID)
	if tenantID == "" {
		tenantID = c.GetHeader("X-Tenant-ID")
	}
	storeID := c.GetString(auth.CtxStoreID)

	var body createRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body", "message": err.Error()})
		return
	}
	body.Subject = strings.TrimSpace(body.Subject)
	body.Message = strings.TrimSpace(body.Message)
	body.Name = strings.TrimSpace(body.Name)
	body.Email = strings.TrimSpace(body.Email)
	body.Reason = strings.TrimSpace(body.Reason)
	body.StatusInfo = strings.TrimSpace(body.StatusInfo)
	body.DOB = strings.TrimSpace(body.DOB)

	// A new thread must have something to say — an empty opener would leave
	// staff with nothing to accept.
	if body.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "empty_message"})
		return
	}

	// Intake is mandatory: every new case carries a reason + status so
	// the case lands in the inbox with context. The (tenant, reason)
	// pair must be in the per-tenant whitelist (see model.go's
	// TenantReasons) so a fanzone customer can't smuggle in a mark8ly
	// reason and vice versa. DOB is only demanded when the reason is
	// flagged as needing an account/order lookup, which is currently
	// mark8ly-only.
	if body.Reason == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "reason_required"})
		return
	}
	if !IsReasonAllowed(tenantID, body.Reason) {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "reason_invalid",
			"message": "Reason is not allowed for this product.",
		})
		return
	}
	if body.StatusInfo == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "status_required"})
		return
	}
	if DOBRequiredFor(tenantID, body.Reason) && body.DOB == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "dob_required",
			"message": "Date of birth is required for order-related cases.",
		})
		return
	}

	// Authorisation: either the caller is a logged-in customer (identity
	// forwarded by the storefront proxy) or they completed the OTP flow.
	//
	// Logged-in users are already email-verified by the storefront's sign-in
	// path, so we accept them without a second factor. For everyone else a
	// valid OTP prevents trivial bot/spam creation of threads.
	verifiedUserID := c.GetString(auth.CtxUserID)
	verifiedEmail := c.GetString(auth.CtxUserEmail)
	if verifiedUserID == "" {
		// Anonymous — must bring an OTP.
		if body.Email == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email_required"})
			return
		}
		if h.d.OTP == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "otp_not_configured"})
			return
		}
		if err := h.d.OTP.Verify(c.Request.Context(), otp.VerifyInput{
			TenantID: tenantID,
			StoreID:  storeID,
			Email:    body.Email,
			Code:     body.OTPCode,
		}); err != nil {
			mapOTPErrorToResponse(c, err)
			return
		}
		// Anonymous caller: prefer the email they typed.
		verifiedEmail = body.Email
	} else if body.Email == "" {
		// Logged-in user — fall back to the email the proxy forwarded when
		// the client didn't echo it (typical for the auto-start path).
		body.Email = verifiedEmail
	}

	raw, tok, err := h.d.Signer.Issue(tenantID, storeID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "issue_session"})
		return
	}

	now := time.Now().UTC()
	caseID, err := GenerateCaseID()
	if err != nil {
		h.d.Logger.Error("otto: gen case id", "err", err)
		caseID = "CS-" + time.Now().UTC().Format("060102") + "-0000"
	}
	conv := &Conversation{
		ID:            uuid.NewString(),
		CaseID:        caseID,
		TenantID:      tenantID,
		StoreID:       storeID,
		Status:        StatusPending,
		Subject:       body.Subject,
		Customer: Customer{
			SessionToken: tok.ID,
			UserID:       verifiedUserID,
			Name:         body.Name,
			Email:        body.Email,
		},
		Intake: &IntakeForm{
			Reason:      body.Reason,
			Status:      body.StatusInfo,
			DOB:         body.DOB,
			SubmittedAt: now,
		},
		CreatedAt:             now,
		UpdatedAt:             now,
		LastMessageAt:         now,
		LastCustomerMessageAt: now,
		MessageCount:          1,
		// The new message counts as unread for staff since no one has seen it.
		UnreadCountStaff: 1,
	}
	if err := h.d.Conversations.Insert(c.Request.Context(), conv); err != nil {
		h.d.Logger.Error("otto: create conversation", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}

	firstMsg := &message.Message{
		ID:             uuid.NewString(),
		ConversationID: conv.ID,
		TenantID:       tenantID,
		StoreID:        storeID,
		SenderType:     message.SenderCustomer,
		SenderID:       conv.Customer.UserID,
		SenderName:     displayName(body.Name, body.Email, "Customer"),
		Body:           body.Message,
		CreatedAt:      now,
	}
	if err := h.d.Messages.Insert(c.Request.Context(), firstMsg); err != nil {
		h.d.Logger.Error("otto: insert first message", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}

	setSessionCookie(c, h.d.CookieName, raw, h.d.CookieDomain, h.d.CookieSecure, tok.Expiry)

	// Notify any staff already watching the inbox that a new pending thread
	// has arrived. Delivery is best-effort.
	h.d.Hub.Broadcast(hub.RoomInbox(tenantID, storeID), hub.Envelope{
		Type:    event.TypeConversationCreated,
		Payload: map[string]any{"conversation": conv, "first_message": firstMsg},
	})

	h.emitCustomerAudit(c, conv, AuditCaseCreated, map[string]any{
		"reason": body.Reason,
	})

	c.JSON(http.StatusCreated, gin.H{
		"conversation":  conv,
		"first_message": firstMsg,
	})
}

// resume looks at the otto_session cookie on the inbound request (if any)
// and returns the most recent open thread + its messages for that
// session. The cookie is validated against the current tenant+store
// scope — a cookie minted for another store can't re-use this endpoint.
//
// When there's no cookie, no open thread, or the cookie is for a
// different scope, we return {conversation: null} with 200 so the widget
// can cleanly fall through to its "start a fresh conversation" flow
// without treating a missing thread as an error.
func (h *StorefrontHandler) resume(c *gin.Context) {
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)

	raw, err := c.Cookie(h.d.CookieName)
	if err != nil || raw == "" {
		raw = c.GetHeader("X-Otto-Session")
	}
	if raw == "" {
		c.JSON(http.StatusOK, gin.H{"conversation": nil})
		return
	}
	tok, err := h.d.Signer.Parse(raw, tenantID, storeID)
	if err != nil {
		// Expired / cross-scope / tampered cookie. Surface as "nothing to
		// resume" — the widget will issue a fresh cookie on first message.
		c.JSON(http.StatusOK, gin.H{"conversation": nil})
		return
	}
	conv, err := h.d.Conversations.LatestOpenForSession(c.Request.Context(), tenantID, storeID, tok.ID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusOK, gin.H{"conversation": nil})
			return
		}
		h.d.Logger.Error("otto: resume lookup", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "resume_failed"})
		return
	}
	msgs, err := h.d.Messages.ListByConversation(c.Request.Context(), tenantID, storeID, conv.ID, 200)
	if err != nil {
		h.d.Logger.Error("otto: resume messages", "err", err)
		msgs = nil
	}
	if msgs == nil {
		msgs = []message.Message{}
	}
	c.JSON(http.StatusOK, gin.H{
		"conversation": conv,
		"messages":     msgs,
	})
}

func (h *StorefrontHandler) get(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	// Customer opened the thread in their UI — they've seen all messages.
	_ = h.d.Conversations.ClearUnread(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, AudienceCustomer)
	c.JSON(http.StatusOK, gin.H{"conversation": conv})
}

func (h *StorefrontHandler) listMessages(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	msgs, err := h.d.Messages.ListByConversation(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, 200)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "list_failed"})
		return
	}
	if msgs == nil {
		msgs = []message.Message{}
	}
	c.JSON(http.StatusOK, gin.H{"messages": msgs})
}

type postMessageRequest struct {
	Body string `json:"body"`
}

func (h *StorefrontHandler) postMessage(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	if conv.Status == StatusClosed {
		c.JSON(http.StatusConflict, gin.H{"error": "thread_closed"})
		return
	}

	var body postMessageRequest
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
		SenderType:     message.SenderCustomer,
		SenderID:       conv.Customer.UserID,
		SenderName:     displayName(conv.Customer.Name, conv.Customer.Email, "Customer"),
		Body:           body.Body,
		CreatedAt:      now,
	}
	if err := h.d.Messages.Insert(c.Request.Context(), msg); err != nil {
		h.d.Logger.Error("otto: insert customer msg", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "persist_failed"})
		return
	}
	if err := h.d.Conversations.BumpOnMessage(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, AudienceStaff, now); err != nil {
		h.d.Logger.Warn("otto: bump conversation", "err", err)
	}
	// Track last-customer-activity so the inactivity sweeper can
	// distinguish "customer typed recently" from "staff sent a reply
	// but the customer walked away".
	if err := h.d.Conversations.BumpCustomerActivity(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, now); err != nil {
		h.d.Logger.Warn("otto: bump customer activity", "err", err)
	}

	// Fan out to whoever is watching.
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

func (h *StorefrontHandler) queue(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	snap, err := h.d.Conversations.QueuePosition(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID)
	if err != nil {
		h.d.Logger.Error("otto: queue position", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "queue_lookup_failed"})
		return
	}
	// "All agents busy" — set when there's no staff currently free
	// to accept. Used by the widget to swap the "position X" copy
	// for a clearer "every agent is with another customer" state.
	allBusy := false
	if h.d.Availability != nil {
		if avail, err := h.d.Availability.CountAvailable(c.Request.Context(), conv.TenantID, conv.StoreID); err == nil {
			allBusy = avail == 0
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"status":                 conv.Status,
		"position":               snap.Position,
		"total_pending":          snap.TotalPending,
		"estimated_wait_seconds": snap.EstimatedWait,
		"all_busy":               allBusy,
	})
}

type feedbackRequest struct {
	CallRating    int    `json:"call_rating"`
	QueryResolved bool   `json:"query_resolved"`
	StaffRating   int    `json:"staff_rating"`
	Comments      string `json:"comments"`
}

func (h *StorefrontHandler) feedback(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	if conv.Status != StatusClosed {
		c.JSON(http.StatusConflict, gin.H{
			"error":   "not_closed",
			"message": "Feedback can only be submitted after the case is closed.",
		})
		return
	}
	if conv.Feedback != nil {
		// Idempotent — treat a repeated submit as a no-op so a
		// double-tap on the submit button doesn't error.
		c.JSON(http.StatusOK, gin.H{"conversation": conv})
		return
	}
	var body feedbackRequest
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid_body"})
		return
	}
	if body.CallRating < 0 || body.CallRating > 5 || body.StaffRating < 0 || body.StaffRating > 5 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "rating_out_of_range"})
		return
	}
	fb := Feedback{
		CallRating:    body.CallRating,
		QueryResolved: body.QueryResolved,
		StaffRating:   body.StaffRating,
		Comments:      strings.TrimSpace(body.Comments),
	}
	updated, err := h.d.Conversations.SubmitFeedback(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID, fb)
	if err != nil {
		h.d.Logger.Error("otto: submit feedback", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "feedback_failed"})
		return
	}
	// Let any admin watching see the feedback come in live.
	h.d.Hub.Broadcast(hub.RoomInbox(conv.TenantID, conv.StoreID), hub.Envelope{
		Type:    event.TypeConversationUpdated,
		Payload: map[string]any{"conversation": updated},
	})
	h.emitCustomerAudit(c, updated, AuditFeedbackSubmitted, map[string]any{
		"call_rating":    fb.CallRating,
		"query_resolved": fb.QueryResolved,
		"staff_rating":   fb.StaffRating,
	})
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

func (h *StorefrontHandler) close(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	updated, err := h.d.Conversations.Close(c.Request.Context(), conv.TenantID, conv.StoreID, conv.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "close_failed"})
		return
	}
	// Release the staff back to the pool on customer-initiated close,
	// matching what the admin close path does.
	if h.d.Availability != nil && updated.Assignee != nil {
		if releaseErr := h.d.Availability.ReleaseCase(c.Request.Context(), updated.TenantID, updated.StoreID, updated.Assignee.UserID); releaseErr != nil {
			h.d.Logger.Warn("otto: release availability on customer close", "err", releaseErr)
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
	h.emitCustomerAudit(c, updated, AuditCaseClosedByCust, nil)
	c.JSON(http.StatusOK, gin.H{"conversation": updated})
}

// websocketUpgrader is shared — no origin checks here because the Next.js
// proxy handles origin before the request reaches us, and CORS is
// configured at the gin engine level for direct WS clients.
var websocketUpgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

// wsTicket mints a short-lived signed ticket the browser carries in the
// WebSocket upgrade URL. Runs with full cookie + header auth applied — the
// caller must already own the conversation.
func (h *StorefrontHandler) wsTicket(c *gin.Context) {
	conv, ok := h.loadForCustomer(c)
	if !ok {
		return
	}
	raw, _, err := h.d.Tickets.Issue(session.Ticket{
		Audience:       session.TicketAudienceCustomer,
		TenantID:       conv.TenantID,
		StoreID:        conv.StoreID,
		UserID:         conv.Customer.UserID,
		UserEmail:      conv.Customer.Email,
		UserName:       conv.Customer.Name,
		ConversationID: conv.ID,
		SessionToken:   c.GetString(auth.CtxSessionToken),
	})
	if err != nil {
		h.d.Logger.Error("otto: mint customer ws ticket", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "ticket_mint_failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ticket": raw})
}

// websocket upgrades the connection. Auth comes from the ?ticket= query
// parameter — the WS path is NOT behind the CustomerContext /
// RequireCustomerSession middleware because Istio routes /api/v1/.../ws
// traffic straight here without injecting the proxy headers.
func (h *StorefrontHandler) websocket(c *gin.Context) {
	raw := c.Query("ticket")
	tok, err := h.d.Tickets.Parse(raw)
	if err != nil || tok.Audience != session.TicketAudienceCustomer {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "bad_ticket"})
		return
	}
	convID := c.Param("id")
	if tok.ConversationID != convID {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "ticket_conversation_mismatch"})
		return
	}
	// Defence in depth: look up the conversation and confirm the ticket's
	// session token still owns it.
	conv, err := h.d.Conversations.GetForCustomer(c.Request.Context(), tok.TenantID, tok.StoreID, convID, tok.SessionToken)
	if err != nil {
		c.AbortWithStatusJSON(http.StatusNotFound, gin.H{"error": "not_found"})
		return
	}
	conn, err := websocketUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	client := h.d.Hub.NewClient(conn, map[string]string{
		"role":            "customer",
		"conversation_id": conv.ID,
		"tenant_id":       conv.TenantID,
	})
	h.d.Hub.Subscribe(client, hub.RoomConversation(conv.ID))
	client.Run(h.d.Hub)
}

// loadForCustomer fetches a conversation and verifies the caller's session
// token owns it. Returns false after writing an error response.
func (h *StorefrontHandler) loadForCustomer(c *gin.Context) (*Conversation, bool) {
	id := c.Param("id")
	tenantID := c.GetString(auth.CtxTenantID)
	storeID := c.GetString(auth.CtxStoreID)
	token := c.GetString(auth.CtxSessionToken)

	conv, err := h.d.Conversations.GetForCustomer(c.Request.Context(), tenantID, storeID, id, token)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			c.JSON(http.StatusNotFound, gin.H{"error": "not_found"})
			return nil, false
		}
		h.d.Logger.Error("otto: load conversation", "err", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "lookup_failed"})
		return nil, false
	}
	return conv, true
}

// setSessionCookie writes the signed session cookie. HttpOnly + SameSite=Lax
// means the storefront JS never reads the cookie (mobile apps opt into the
// X-Otto-Session header path instead).
func setSessionCookie(c *gin.Context, name, value, domain string, secure bool, expiry time.Time) {
	maxAge := int(time.Until(expiry).Seconds())
	if maxAge < 60 {
		maxAge = 60
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie(name, value, maxAge, "/", domain, secure, true)
}

func displayName(name, email, fallback string) string {
	name = strings.TrimSpace(name)
	if name != "" {
		return name
	}
	if email != "" {
		return email
	}
	return fallback
}

// mapOTPErrorToResponse translates an OTP verification error into the
// JSON/status the widget understands. Keeping this in one place means
// the error contract between Go and TS stays consistent.
func mapOTPErrorToResponse(c *gin.Context, err error) {
	switch {
	case errors.Is(err, otp.ErrInvalidCode):
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "otp_invalid",
			"message": "that code didn't match — double-check and try again",
		})
	case errors.Is(err, otp.ErrExpired):
		c.JSON(http.StatusUnauthorized, gin.H{
			"error":   "otp_expired",
			"message": "that code has expired — request a new one",
		})
	case errors.Is(err, otp.ErrTooManyAttempts):
		c.JSON(http.StatusTooManyRequests, gin.H{
			"error":   "otp_too_many_attempts",
			"message": "too many attempts — request a new code",
		})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "otp_verify_failed"})
	}
}
