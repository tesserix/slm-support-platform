# Otto Platform Inbox API Implementation Plan (Phase 1 of 4)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a cross-tenant staff inbox API to otto's `/api/v1/platform/otto` surface so Tesserix platform admins can list, accept, reply to, and close support conversations from every product tenant.

**Architecture:** A new `PlatformHandler` loads conversations without tenant scope, then delegates the per-conversation actions (get/messages/accept/reply/close) to the existing `AdminHandler` methods by injecting the loaded conversation's tenant+store into the gin context — zero duplication of the accept/reply/close business logic. A new hub room `inbox-platform` receives a copy of every inbox broadcast via a `BroadcastInbox` helper, and a new `platform` ticket audience keeps platform WebSocket tickets and tenant-staff tickets mutually unreplayable.

**Tech Stack:** Go 1.26, Gin, MongoDB (mongo-driver), gorilla/websocket, HMAC tickets. Repo: `slm-support-platform`, service: `services/otto`.

**Spec:** `docs/superpowers/specs/2026-07-25-otto-platform-inbox-design.md`

## Global Constraints

- Working directory for all commands: `/Users/samyakrout/Desktop/samyak-work/projects/new-repos/slm-support-platform/services/otto`
- Go 1.26 (`services/otto/go.mod`); verify with `go build ./...`, `go vet ./...`, `go test ./...` only — NEVER run docker/sandboxctl builds or deploys (the user does those)
- Git identity before any commit: `git config user.name "sam123ben" && git config user.email "samyak.rout@gmail.com"`
- NEVER mention Claude/AI/Co-Authored-By in commits or code comments
- Conventional commits, `feat(otto):` / `test(otto):` / `docs(otto):` scope
- `PlatformAuth`'s deny-on-empty-secret rule must survive: every new platform endpoint stays unreachable when `INTERNAL_AUTH_SECRET` is empty
- This repo has NO Mongo test harness (existing tests are pure-unit: config, contentguard, mailer, httpserver, hub). Repository methods and handler wiring are verified by `go build` + the Task 6 live smoke run against a local `mongo:7` container — do not invent a mongo mock layer
- The existing tenant-scoped admin/storefront surfaces must not change behavior (only the internal rename `Broadcast(RoomInbox(...))` → `BroadcastInbox(...)`, which is broadcast-equivalent plus one extra room)

---

### Task 1: `platform` ticket audience

**Files:**
- Modify: `internal/session/ticket.go:16-21`
- Test: `internal/session/ticket_test.go` (create)

**Interfaces:**
- Consumes: existing `TicketSigner.Issue(Ticket) (string, Ticket, error)`, `TicketSigner.Parse(string) (Ticket, error)`
- Produces: `session.TicketAudiencePlatform session.TicketAudience = "platform"` — Task 5's handlers and tests reference this exact constant. Platform inbox tickets use the sentinel scope `TenantID: "*", StoreID: "*"` (Issue rejects empty strings; `"*"` is the agreed cross-tenant sentinel).

- [ ] **Step 1: Write the failing test**

Create `internal/session/ticket_test.go`:

```go
package session

import (
	"testing"
	"time"
)

func newTestSigner() *TicketSigner {
	return NewTicketSigner("test-secret-0123456789", 2*time.Minute)
}

func TestPlatformTicketRoundTrip(t *testing.T) {
	s := newTestSigner()
	raw, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "*",
		StoreID:  "*",
		UserID:   "admin-1",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	tok, err := s.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if tok.Audience != TicketAudiencePlatform {
		t.Fatalf("audience = %q, want %q", tok.Audience, TicketAudiencePlatform)
	}
	if tok.TenantID != "*" || tok.StoreID != "*" {
		t.Fatalf("scope = %q/%q, want */*", tok.TenantID, tok.StoreID)
	}
	if tok.UserID != "admin-1" {
		t.Fatalf("user = %q, want admin-1", tok.UserID)
	}
}

// The audience constants must stay distinct — WS handlers use them to
// reject a staff ticket on a platform socket and vice versa.
func TestTicketAudiencesAreDistinct(t *testing.T) {
	if TicketAudiencePlatform == TicketAudienceStaff ||
		TicketAudiencePlatform == TicketAudienceCustomer {
		t.Fatal("platform audience must not collide with staff/customer")
	}
}

func TestTamperedTicketRejected(t *testing.T) {
	s := newTestSigner()
	raw, _, err := s.Issue(Ticket{
		Audience: TicketAudiencePlatform,
		TenantID: "*",
		StoreID:  "*",
	})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	if _, err := s.Parse(raw + "x"); err == nil {
		t.Fatal("tampered ticket parsed without error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/session/ -run 'TestPlatformTicket|TestTicketAudiences|TestTampered' -v`
Expected: FAIL — `undefined: TicketAudiencePlatform` (compile error)

- [ ] **Step 3: Add the constant**

In `internal/session/ticket.go`, extend the audience const block:

```go
const (
	TicketAudienceCustomer TicketAudience = "customer"
	TicketAudienceStaff    TicketAudience = "staff"
	// TicketAudiencePlatform is minted for the cross-tenant platform
	// inbox (tesserix-home admins). Platform WS endpoints accept ONLY
	// this audience, and tenant-scoped admin WS endpoints reject it —
	// a platform ticket can never be replayed against a store inbox
	// or vice versa. Inbox-wide platform tickets carry the sentinel
	// scope tenant="*" store="*"; per-thread ones carry the real scope.
	TicketAudiencePlatform TicketAudience = "platform"
)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/session/ -v`
Expected: PASS (all three new tests)

- [ ] **Step 5: Commit**

```bash
git config user.name "sam123ben" && git config user.email "samyak.rout@gmail.com"
git add internal/session/ticket.go internal/session/ticket_test.go
git commit -m "feat(otto): add platform ticket audience for cross-tenant WS"
```

---

### Task 2: `PlatformStaff` middleware

**Files:**
- Modify: `internal/auth/middleware.go` (add function after `PlatformAuth`, line 77)
- Test: `internal/auth/middleware_test.go` (create)

**Interfaces:**
- Consumes: existing context keys `CtxUserID`, `CtxUserEmail`, `CtxUserName`, `CtxUserRole`, helper `constantTimeEqual`, `respondUnauthorized`
- Produces: `auth.PlatformStaff(internalSecret string) gin.HandlerFunc` — mandatory secret (empty denies, same rule as `PlatformAuth`) AND mandatory `X-User-Id`; sets `CtxUserID` and optional `CtxUserEmail`/`CtxUserName`/`CtxUserRole`. Task 5 mounts the platform inbox REST group behind exactly this middleware.

- [ ] **Step 1: Write the failing test**

Create `internal/auth/middleware_test.go`:

```go
package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const testSecret = "internal-secret"

func platformStaffRouter(secret string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/p")
	g.Use(PlatformStaff(secret))
	g.GET("/ok", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"user_id": c.GetString(CtxUserID),
			"email":   c.GetString(CtxUserEmail),
		})
	})
	return r
}

func doReq(r *gin.Engine, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/p/ok", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Empty configured secret must deny even a matching empty header —
// the cross-tenant surface never falls open (same rule as PlatformAuth).
func TestPlatformStaffDeniesOnEmptyConfiguredSecret(t *testing.T) {
	r := platformStaffRouter("")
	w := doReq(r, map[string]string{"X-Internal-Auth": "", "X-User-Id": "u1"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestPlatformStaffDeniesWrongSecret(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{"X-Internal-Auth": "nope", "X-User-Id": "u1"})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

// Secret alone is not enough — accept/reply/close must be attributable,
// so a missing X-User-Id denies.
func TestPlatformStaffRequiresUserID(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{"X-Internal-Auth": testSecret})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}

func TestPlatformStaffSetsIdentityContext(t *testing.T) {
	r := platformStaffRouter(testSecret)
	w := doReq(r, map[string]string{
		"X-Internal-Auth": testSecret,
		"X-User-Id":       "admin-1",
		"X-User-Email":    "admin@tesserix.app",
	})
	if w.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{`"user_id":"admin-1"`, `"email":"admin@tesserix.app"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %s missing %s", body, want)
		}
	}
}

// Regression: PlatformAuth (stats endpoint) also denies on empty secret.
func TestPlatformAuthDeniesOnEmptyConfiguredSecret(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/p")
	g.Use(PlatformAuth(""))
	g.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })
	w := doReq(r, map[string]string{"X-Internal-Auth": ""})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", w.Code)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/auth/ -v`
Expected: FAIL — `undefined: PlatformStaff` (compile error)

- [ ] **Step 3: Implement the middleware**

In `internal/auth/middleware.go`, add directly after `PlatformAuth` (after line 77):

```go
// PlatformStaff gates the cross-tenant platform INBOX endpoints
// (list/accept/reply/close). Two requirements, both mandatory:
//   1. the internal shared secret — empty configured secret denies,
//      same rule as PlatformAuth: a cross-tenant surface must never
//      fall open;
//   2. a staff identity (X-User-Id) forwarded by the tesserix-home
//      admin proxy — accept/reply/close must be attributable to a
//      human, so an anonymous secret-holder is rejected.
func PlatformStaff(internalSecret string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if internalSecret == "" || !constantTimeEqual(c.GetHeader("X-Internal-Auth"), internalSecret) {
			respondUnauthorized(c)
			return
		}
		userID := c.GetHeader("X-User-Id")
		if userID == "" {
			respondUnauthorized(c)
			return
		}
		c.Set(CtxUserID, userID)
		if email := c.GetHeader("X-User-Email"); email != "" {
			c.Set(CtxUserEmail, email)
		}
		if name := c.GetHeader("X-User-Name"); name != "" {
			c.Set(CtxUserName, name)
		}
		if role := c.GetHeader("X-User-Role"); role != "" {
			c.Set(CtxUserRole, role)
		}
		c.Next()
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/auth/ -v`
Expected: PASS (all 6 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/auth/middleware.go internal/auth/middleware_test.go
git commit -m "feat(otto): PlatformStaff middleware for attributed cross-tenant access"
```

---

### Task 3: Platform inbox room + `BroadcastInbox` fan-out

**Files:**
- Modify: `internal/hub/json.go` (add room key)
- Modify: `internal/hub/hub.go` (add `BroadcastInbox` after `Broadcast`, line 144)
- Modify (mechanical sweep, 10 call sites):
  - `internal/conversation/admin_handler.go:273,349,370,400,571`
  - `internal/conversation/sweeper.go:111`
  - `internal/conversation/storefront_handler.go:337,495,579,612`
  - `internal/changestream/watcher.go:167`
- Test: `internal/hub/broadcastinbox_test.go` (create)

**Interfaces:**
- Consumes: `Hub.Broadcast(room string, env Envelope)`, `RoomInbox(tenantID, storeID string) string`, `Hub.NewChannelClient(meta) *Client`, `Client.Channel() <-chan []byte`
- Produces:
  - `hub.RoomPlatformInbox() string` returning the constant room key `"inbox-platform"` (distinct prefix — cannot collide with `RoomInbox("platform", ...)` = `"inbox:platform:..."`)
  - `hub.(*Hub).BroadcastInbox(tenantID, storeID string, env Envelope)` — broadcasts env to BOTH `RoomInbox(tenantID, storeID)` and `RoomPlatformInbox()`. Task 5's platform WS subscribes to `RoomPlatformInbox()`.

- [ ] **Step 1: Write the failing test**

Create `internal/hub/broadcastinbox_test.go`:

```go
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
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/hub/ -v`
Expected: FAIL — `undefined: RoomPlatformInbox` / `undefined: (*Hub).BroadcastInbox` (compile error)

- [ ] **Step 3: Implement room key + helper**

In `internal/hub/json.go`, append:

```go
// RoomPlatformInbox is the cross-tenant inbox room for platform
// super-admins (tesserix-home). Every inbox broadcast is mirrored here
// by Hub.BroadcastInbox. The "inbox-platform" key deliberately uses a
// different prefix shape from RoomInbox's "inbox:<tenant>:<store>" so
// no tenant id (even one named "platform") can collide with it.
func RoomPlatformInbox() string {
	return "inbox-platform"
}
```

In `internal/hub/hub.go`, add after `Broadcast` (after line 144):

```go
// BroadcastInbox fans an inbox event out to the tenant's own staff room
// AND the cross-tenant platform inbox room. Every code path that used
// to broadcast to RoomInbox directly goes through here so platform
// admins see the same live updates tenant staff do. env is passed by
// value, so each Broadcast stamps its own Room field.
func (h *Hub) BroadcastInbox(tenantID, storeID string, env Envelope) {
	h.Broadcast(RoomInbox(tenantID, storeID), env)
	h.Broadcast(RoomPlatformInbox(), env)
}
```

- [ ] **Step 4: Run hub tests to verify they pass**

Run: `go test ./internal/hub/ -v`
Expected: PASS (both new tests + existing channelclient tests)

- [ ] **Step 5: Sweep the 10 broadcast call sites**

Every `X.Broadcast(hub.RoomInbox(A, B), env)` becomes `X.BroadcastInbox(A, B, env)`. Find them with:

```bash
grep -rn "Broadcast(hub.RoomInbox" internal/
```

Exact edits (receiver spelled as found at each site):

- `internal/conversation/admin_handler.go:273` → `h.d.Hub.BroadcastInbox(updated.TenantID, updated.StoreID, hub.Envelope{...})`
- `internal/conversation/admin_handler.go:349` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/conversation/admin_handler.go:370` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/conversation/admin_handler.go:400` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/conversation/admin_handler.go:571` → `h.d.Hub.BroadcastInbox(updated.TenantID, updated.StoreID, hub.Envelope{...})`
- `internal/conversation/sweeper.go:111` → `s.Hub.BroadcastInbox(updated.TenantID, updated.StoreID, hub.Envelope{...})`
- `internal/conversation/storefront_handler.go:337` → `h.d.Hub.BroadcastInbox(tenantID, storeID, hub.Envelope{...})`
- `internal/conversation/storefront_handler.go:495` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/conversation/storefront_handler.go:579` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/conversation/storefront_handler.go:612` → `h.d.Hub.BroadcastInbox(conv.TenantID, conv.StoreID, hub.Envelope{...})`
- `internal/changestream/watcher.go:167` → `w.Hub.BroadcastInbox(doc.TenantID, doc.StoreID, envelope)` (keep its surrounding `if` exactly as is)

In each case only the function call wrapper changes — the `hub.Envelope{...}` literal argument is kept verbatim. `Subscribe(..., hub.RoomInbox(...))` calls are NOT touched.

- [ ] **Step 6: Verify the sweep is complete and everything builds**

Run: `grep -rn "Broadcast(hub.RoomInbox" internal/ ; go build ./... && go vet ./... && go test ./...`
Expected: grep prints nothing; build/vet/test all pass

- [ ] **Step 7: Commit**

```bash
git add internal/hub/ internal/conversation/ internal/changestream/
git commit -m "feat(otto): mirror inbox broadcasts to a cross-tenant platform room"
```

---

### Task 4: Cross-tenant repository queries + Mongo index

**Files:**
- Modify: `internal/conversation/repo.go` (add after `ListInbox`, line 160)
- Modify: `internal/mongo/client.go` (`EnsureIndexes`, conversations index list ending line 112)

**Interfaces:**
- Consumes: `Conversation`, `Status`, `ErrNotFound`, mongo-driver types already imported in `repo.go`
- Produces (Task 5 calls these):
  - `type PlatformListParams struct { TenantID string; Status Status; AssigneeUserID string; OnlyUnassigned bool; Limit int64 }` — `TenantID` empty = all tenants
  - `(*Repository).ListPlatformInbox(ctx context.Context, p PlatformListParams) ([]Conversation, error)`
  - `(*Repository).GetByIDAnyTenant(ctx context.Context, id string) (*Conversation, error)` — returns `ErrNotFound` when absent

No unit test (Global Constraints: no Mongo harness in this repo — both methods are thin filter-builders verified by the Task 6 smoke run).

- [ ] **Step 1: Add the repository methods**

In `internal/conversation/repo.go`, insert after `ListInbox` (line 160):

```go
// PlatformListParams filters the cross-tenant platform inbox. Unlike
// ListInboxParams there is no mandatory scope: an empty TenantID means
// "every tenant otto serves". Only the platform surface (PlatformStaff
// gate) may reach this — tenant staff always go through ListInbox.
type PlatformListParams struct {
	TenantID       string // empty = all tenants
	Status         Status // empty = all
	AssigneeUserID string // empty = any assignee
	OnlyUnassigned bool
	Limit          int64
}

// ListPlatformInbox returns conversations across every tenant for the
// platform super-admin inbox, newest activity first.
func (r *Repository) ListPlatformInbox(ctx context.Context, p PlatformListParams) ([]Conversation, error) {
	filter := bson.M{}
	if p.TenantID != "" {
		filter["tenant_id"] = p.TenantID
	}
	if p.Status != "" {
		filter["status"] = p.Status
	}
	switch {
	case p.OnlyUnassigned:
		filter["assignee"] = bson.M{"$exists": false}
	case p.AssigneeUserID != "":
		filter["assignee.user_id"] = p.AssigneeUserID
	}
	limit := p.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "last_message_at", Value: -1}}).
		SetLimit(limit)
	cur, err := r.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []Conversation
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetByIDAnyTenant loads a conversation without tenant scope — platform
// super-admins address threads by id alone. The caller (platform
// handler) re-injects the row's real tenant+store before any write so
// every downstream repo call stays scoped.
func (r *Repository) GetByIDAnyTenant(ctx context.Context, id string) (*Conversation, error) {
	var c Conversation
	if err := r.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&c); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &c, nil
}
```

- [ ] **Step 2: Add the global inbox index**

In `internal/mongo/client.go`, append to the `convIdx` slice (before the closing `}` at line 112):

```go
		{
			// Platform inbox: cross-tenant "waiting/active, newest first"
			// list — no tenant prefix, so the tenant-scoped indexes can't
			// serve it. Unfiltered lists are limit-capped (<=200) and rare.
			Keys: bson.D{
				{Key: "status", Value: 1},
				{Key: "last_message_at", Value: -1},
			},
			Options: options.Index().SetName("status_last_msg_platform"),
		},
```

- [ ] **Step 3: Verify build + full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass, no new warnings

- [ ] **Step 4: Commit**

```bash
git add internal/conversation/repo.go internal/mongo/client.go
git commit -m "feat(otto): cross-tenant conversation queries and platform inbox index"
```

---

### Task 5: `PlatformHandler` + route wiring

**Files:**
- Create: `internal/conversation/platform_handler.go`
- Modify: `cmd/server/main.go:162-165` (platform group) and `cmd/server/main.go:196-200` (WS groups)

**Interfaces:**
- Consumes:
  - Task 1: `session.TicketAudiencePlatform`
  - Task 2: `auth.PlatformStaff` (mounted in main.go)
  - Task 3: `hub.RoomPlatformInbox()`, delegated admin methods already emit via `BroadcastInbox`
  - Task 4: `PlatformListParams`, `ListPlatformInbox`, `GetByIDAnyTenant`
  - Existing: `AdminHandler` unexported methods `get`, `listMessages`, `accept`, `postMessage`, `close` (same package), `AdminDeps`, `websocketUpgrader` (package-level upgrader used by the other handlers), `auth.CtxTenantID/CtxStoreID/CtxUserID/...`
- Produces:
  - `NewPlatformHandler(admin *AdminHandler, d AdminDeps) *PlatformHandler`
  - `(*PlatformHandler).Register(r *gin.RouterGroup)` — REST: `GET /conversations`, `GET /conversations/:id`, `GET /conversations/:id/messages`, `POST /conversations/:id/accept`, `POST /conversations/:id/messages`, `POST /conversations/:id/close`, `POST /ws-ticket`, `POST /conversations/:id/ws-ticket`
  - `(*PlatformHandler).RegisterWS(r *gin.RouterGroup)` — `GET /ws`, `GET /conversations/:id/ws`
  - Wire contract for Phase 3 (tesserix-home proxy): identical request/response bodies to the tenant admin surface, plus each conversation JSON already carries `tenant_id`

- [ ] **Step 1: Write the handler**

Create `internal/conversation/platform_handler.go`:

```go
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
```

Note: `websocketUpgrader` is the package-level upgrader the admin/storefront handlers already share — confirm its exact identifier with `grep -n "websocketUpgrader" internal/conversation/*.go` and reuse it (do not create a second upgrader).

- [ ] **Step 2: Wire the routes in `cmd/server/main.go`**

Replace lines 162-165:

```go
	platform := r.Group("/api/v1/platform/otto")
	platform.Use(auth.PlatformAuth(cfg.InternalAuthSecret))
	adminHandler.RegisterPlatform(platform)
```

with:

```go
	// /stats keeps the identity-less PlatformAuth gate — the analytics
	// proxy doesn't always have a user in hand. The inbox endpoints
	// require an attributed staff identity on top (PlatformStaff).
	platform := r.Group("/api/v1/platform/otto")
	platform.Use(auth.PlatformAuth(cfg.InternalAuthSecret))
	adminHandler.RegisterPlatform(platform)

	platformHandler := conversation.NewPlatformHandler(adminHandler, conversation.AdminDeps{
		Conversations: convRepo,
		Availability:  availRepo,
		Audit:         auditRepo,
		Messages:      msgRepo,
		Hub:           h,
		Tickets:       ticketSigner,
		Logger:        log,
	})
	platformInbox := r.Group("/api/v1/platform/otto")
	platformInbox.Use(auth.PlatformStaff(cfg.InternalAuthSecret))
	platformHandler.Register(platformInbox)
```

Then in the WS block (lines 196-200), after `storefrontHandler.RegisterWS(...)`, add:

```go
	platformHandler.RegisterWS(r.Group("/api/v1/platform/otto"))
```

- [ ] **Step 3: Build, vet, full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass. If gin panics at startup about duplicate routes, a path was double-registered — the three `/api/v1/platform/otto` groups must have disjoint route sets (`/stats` | REST inbox | the two `/ws` routes).

- [ ] **Step 4: Boot-smoke the router registration**

Run: `MONGO_URL=mongodb://localhost:1 CUSTOMER_SESSION_SECRET=dev INTERNAL_AUTH_SECRET=dev timeout 10 go run ./cmd/server 2>&1 | head -20 || true`
Expected: it fails on Mongo connect (no Mongo at port 1) — that is fine. What must NOT appear: a gin `panic: ... handlers are already registered for path ...`. (Route registration happens after Mongo connect, so absence of a route panic is fully confirmed in Task 6's live run.)

- [ ] **Step 5: Commit**

```bash
git add internal/conversation/platform_handler.go cmd/server/main.go
git commit -m "feat(otto): cross-tenant platform inbox endpoints and websockets"
```

---

### Task 6: Live smoke run + README surface docs

**Files:**
- Modify: `README.md` (services/otto — the `## Surface` block)
- No code changes expected; fixes discovered here fold into this task's commit

**Interfaces:**
- Consumes: everything from Tasks 1-5
- Produces: verified end-to-end behavior + documented surface for Phase 3 (tesserix-home proxy) implementers

- [ ] **Step 1: Start Mongo and otto locally**

```bash
docker run --rm -d --name otto-smoke -p 27017:27017 mongo:7
MONGO_URL=mongodb://localhost:27017 MONGO_DATABASE=otto_smoke \
CUSTOMER_SESSION_SECRET=dev-secret-0123456789 \
INTERNAL_AUTH_SECRET=dev-internal HTTP_PORT=8089 ENV=dev \
go run ./cmd/server &
sleep 3
```

(Running a local mongo container for tests is allowed — the prohibition is on image builds/deploys, not on dev dependencies. otto's own README documents this exact workflow.)

- [ ] **Step 2: Create conversations in two different tenants (storefront surface)**

```bash
curl -s -X POST localhost:8089/api/v1/storefront/otto/conversations \
  -H 'Content-Type: application/json' -H 'X-Internal-Auth: dev-internal' \
  -H 'X-Tenant-Id: homechef' -H 'X-Store-Id: default' \
  -H 'X-User-Id: cust-1' -H 'X-User-Email: cust1@example.com' \
  -d '{"message":"My order is stuck on preparing","reason":"other","subject":"Order stuck","name":"Cust One","email":"cust1@example.com"}'

curl -s -X POST localhost:8089/api/v1/storefront/otto/conversations \
  -H 'Content-Type: application/json' -H 'X-Internal-Auth: dev-internal' \
  -H 'X-Tenant-Id: fanzone' -H 'X-Store-Id: default' \
  -H 'X-User-Id: cust-2' -H 'X-User-Email: cust2@example.com' \
  -d '{"message":"Cannot join the battle room","reason":"other","subject":"Room bug","name":"Cust Two","email":"cust2@example.com"}'
```

Expected: two 200/201 JSON responses each containing `"conversation"` with an `"id"` — capture both ids. (Identity headers make these "logged-in" creates, which skip the email OTP path; `reason: "other"` is valid for every tenant via the fallback reason rules.)

- [ ] **Step 3: Platform inbox sees BOTH tenants**

```bash
curl -s 'localhost:8089/api/v1/platform/otto/conversations?status=pending' \
  -H 'X-Internal-Auth: dev-internal' -H 'X-User-Id: admin-1' \
  -H 'X-User-Email: admin@tesserix.app' -H 'X-User-Name: Platform Admin'
```

Expected: `"conversations"` array containing both threads, one with `"tenant_id":"homechef"` and one with `"tenant_id":"fanzone"`. Also verify the tenant filter: `...conversations?tenant=homechef` returns only the homechef one.

- [ ] **Step 4: Auth negative cases**

```bash
# no secret → 401
curl -s -o /dev/null -w '%{http_code}\n' \
  'localhost:8089/api/v1/platform/otto/conversations' -H 'X-User-Id: admin-1'
# secret but no identity → 401
curl -s -o /dev/null -w '%{http_code}\n' \
  'localhost:8089/api/v1/platform/otto/conversations' -H 'X-Internal-Auth: dev-internal'
```

Expected: `401` then `401`.

- [ ] **Step 5: Accept → reply → close through the platform surface**

Using `$CID` = the homechef conversation id from Step 2:

```bash
curl -s -X POST "localhost:8089/api/v1/platform/otto/conversations/$CID/accept" \
  -H 'X-Internal-Auth: dev-internal' -H 'X-User-Id: admin-1' \
  -H 'X-User-Email: admin@tesserix.app' -H 'X-User-Name: Platform Admin'
# expected: conversation.status == "active", assignee.user_id == "admin-1"

curl -s -X POST "localhost:8089/api/v1/platform/otto/conversations/$CID/messages" \
  -H 'Content-Type: application/json' -H 'X-Internal-Auth: dev-internal' \
  -H 'X-User-Id: admin-1' -H 'X-User-Name: Platform Admin' \
  -d '{"body":"Hi, checking your order now."}'
# expected: 201, message.sender_type == "staff", sender_id == "admin-1"

curl -s -X POST "localhost:8089/api/v1/platform/otto/conversations/$CID/close" \
  -H 'X-Internal-Auth: dev-internal' -H 'X-User-Id: admin-1'
# expected: conversation.status == "closed"
```

- [ ] **Step 6: WS ticket audience separation**

```bash
# platform inbox ticket mints
curl -s -X POST 'localhost:8089/api/v1/platform/otto/ws-ticket' \
  -H 'X-Internal-Auth: dev-internal' -H 'X-User-Id: admin-1'
# expected: {"ticket":"..."}

# a platform ticket on the TENANT admin inbox WS is rejected
TICKET=$(curl -s -X POST 'localhost:8089/api/v1/platform/otto/ws-ticket' \
  -H 'X-Internal-Auth: dev-internal' -H 'X-User-Id: admin-1' | python3 -c 'import sys,json;print(json.load(sys.stdin)["ticket"])')
curl -s -o /dev/null -w '%{http_code}\n' "localhost:8089/api/v1/admin/otto/ws?ticket=$TICKET"
# expected: 401 (audience mismatch — staff WS refuses platform tickets)
curl -s -o /dev/null -w '%{http_code}\n' "localhost:8089/api/v1/platform/otto/ws?ticket=$TICKET"
# expected: 400 (correct audience — websocket handshake rejects plain HTTP,
# which proves the ticket gate passed; a 401 here would mean the gate failed)
```

- [ ] **Step 7: Tear down**

```bash
kill %1 2>/dev/null; docker stop otto-smoke
```

- [ ] **Step 8: Document the new surface**

In `services/otto/README.md`, extend the `## Surface` code block after the admin routes:

```
GET    /api/v1/platform/otto/stats                        — cross-tenant analytics rollup
GET    /api/v1/platform/otto/conversations                — platform inbox (all tenants; ?tenant=&status=&assignee=)
GET    /api/v1/platform/otto/conversations/:id            — platform view
GET    /api/v1/platform/otto/conversations/:id/messages   — thread history
POST   /api/v1/platform/otto/conversations/:id/accept     — platform staff accepts
POST   /api/v1/platform/otto/conversations/:id/messages   — platform staff reply
POST   /api/v1/platform/otto/conversations/:id/close      — platform staff closes
POST   /api/v1/platform/otto/ws-ticket                    — mint platform inbox WS ticket
POST   /api/v1/platform/otto/conversations/:id/ws-ticket  — mint platform thread WS ticket
GET    /api/v1/platform/otto/ws                           — platform inbox WS (every tenant)
GET    /api/v1/platform/otto/conversations/:id/ws         — platform per-thread WS
```

And add one sentence to the isolation section:

```
Platform super-admin routes (tesserix-home) are the deliberate exception:
gated by the mandatory internal secret PLUS forwarded staff identity
(X-User-Id), they read across tenants but every write is re-scoped to the
target conversation's own tenant_id + store_id before it executes.
```

- [ ] **Step 9: Final full verification + commit**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all pass.

```bash
git add README.md
git commit -m "docs(otto): document platform inbox surface"
```

---

## Post-plan (NOT part of this plan's execution)

- CI/image: the user pushes and the existing `ci-otto.yml` builds the image; Kargo/ArgoCD promote `support-platform-otto`. Claude never builds or deploys images.
- Phase 2 plan (design-system `OttoInbox` platform mode) is written after this phase ships — its client consumes the exact wire shapes documented in Task 6 Step 8.
- Phase 3 (tesserix-home web proxy + page + tesserix-k8s VirtualService) and Phase 4 (Expo mobile inbox) follow.
