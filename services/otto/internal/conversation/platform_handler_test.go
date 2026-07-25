package conversation

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/tesserix/slm-support-platform/services/otto/internal/session"
)

// These cover only the ticket-audience (and, for per-thread sockets,
// cid-match) gates on the platform/admin WebSocket handlers. Every path
// exercised here aborts BEFORE any repository or Mongo access — see
// PlatformHandler.inboxWebsocket / conversationWebsocket and
// AdminHandler.inboxWebsocket / conversationWebsocket, which all parse +
// validate the ticket first and only reach h.d.Conversations.GetByID (or
// the hub) once the check passes. Nil repos/hub on the handlers built here
// are therefore safe: a passing (200/101) case never occurs in this file.

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func testTicketSigner() *session.TicketSigner {
	return session.NewTicketSigner("test-secret-0123456789", 2*time.Minute)
}

// wsTestRouter wires the admin + platform WS routes exactly as
// cmd/server/main.go does, minus the auth middleware groups (which don't
// apply to the ticket-authed WS routes in the first place).
func wsTestRouter(t *testing.T, signer *session.TicketSigner) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()

	deps := AdminDeps{Tickets: signer, Logger: discardLogger()}
	adminHandler := NewAdminHandler(deps)
	platformHandler := NewPlatformHandler(adminHandler, deps)

	adminHandler.RegisterWS(r.Group("/api/v1/admin/otto"))
	platformHandler.RegisterWS(r.Group("/api/v1/platform/otto"))

	return r
}

func wsGet(r *gin.Engine, path string, ticket string) *httptest.ResponseRecorder {
	q := url.Values{}
	if ticket != "" {
		q.Set("ticket", ticket)
	}
	req := httptest.NewRequest(http.MethodGet, path+"?"+q.Encode(), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPlatformInboxWSRejectsStaffAudienceTicket(t *testing.T) {
	signer := testTicketSigner()
	r := wsTestRouter(t, signer)

	raw, _, err := signer.Issue(session.Ticket{
		Audience: session.TicketAudienceStaff,
		TenantID: "t1",
		StoreID:  "s1",
		UserID:   "u1",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	w := wsGet(r, "/api/v1/platform/otto/ws", raw)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
}

func TestPlatformInboxWSRejectsGarbageTicket(t *testing.T) {
	signer := testTicketSigner()
	r := wsTestRouter(t, signer)

	w := wsGet(r, "/api/v1/platform/otto/ws", "not-a-real-ticket")
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
}

func TestPlatformConversationWSRejectsStaffAudienceTicketBeforeCidCheck(t *testing.T) {
	signer := testTicketSigner()
	r := wsTestRouter(t, signer)

	raw, _, err := signer.Issue(session.Ticket{
		Audience:       session.TicketAudienceStaff,
		TenantID:       "t1",
		StoreID:        "s1",
		UserID:         "u1",
		ConversationID: "c-1",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	w := wsGet(r, "/api/v1/platform/otto/conversations/c-1/ws", raw)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
}

func TestPlatformConversationWSRejectsConversationIDMismatch(t *testing.T) {
	signer := testTicketSigner()
	r := wsTestRouter(t, signer)

	// Platform ticket minted for c-OTHER, presented against c-1's socket.
	raw, _, err := signer.Issue(session.Ticket{
		Audience:       session.TicketAudiencePlatform,
		TenantID:       "t1",
		StoreID:        "s1",
		UserID:         "u1",
		ConversationID: "c-OTHER",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	w := wsGet(r, "/api/v1/platform/otto/conversations/c-1/ws", raw)
	if w.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403 (body %s)", w.Code, w.Body.String())
	}
}

func TestAdminInboxWSRejectsPlatformAudienceTicket(t *testing.T) {
	signer := testTicketSigner()
	r := wsTestRouter(t, signer)

	// Inbox-wide platform ticket, presented against the tenant admin
	// inbox socket.
	raw, _, err := signer.Issue(session.Ticket{
		Audience: session.TicketAudiencePlatform,
		TenantID: "*",
		StoreID:  "*",
		UserID:   "admin-1",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}

	w := wsGet(r, "/api/v1/admin/otto/ws", raw)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 (body %s)", w.Code, w.Body.String())
	}
}
