# Otto platform support inbox — design

**Date:** 2026-07-25
**Status:** Approved (design), pending implementation plan
**Repos touched:** `slm-support-platform`, `design-system`, `tesserix-home`, `tesserix-k8s`

## Context

Otto is Tesserix's shared, tenant-isolated, human support-chat platform (Go +
MongoDB + WebSockets, deployed as `support-platform-otto` in the
`support-platform` namespace). The customer-side widget
(`@tesserix/otto-widget` `<OttoWidget />`) is embedded across products —
tesserix-home, HomeChef web, FanZone, horoscope, stock-analysis,
social-media-scrapper, homechef-web-revival, and mark8ly's merchant→Tesserix
"platform support" chat — each pinning its own tenant id (`homechef`,
`fanzone`, `platform`, …).

The staff side (`<OttoInbox />`) is mounted in exactly one place: mark8ly
admin's live-chat, scoped to a merchant's **own** store. There is **no inbox
anywhere** for the `platform` tenant or the per-product tenants. Customer
chats from HomeChef, FanZone, etc., and merchant→Tesserix chats currently
have no human on the other end.

## Goal

Tesserix admin users (tesserix-home admin, web **and** the Expo mobile admin
app) can see and answer support conversations from customers of **any**
product, in one unified queue.

## Non-goals

- **No AI in the loop now.** slm-router / SLM integration is a future phase;
  its hooks (Mongo change-stream rebroadcast, per-tenant RAG namespaces) are
  untouched and unaffected.
- Otto remains strictly a support-chat service — no ticket creation, no
  agentic actions, no new conversation-creating surfaces.
- Product widgets and their per-product tenants do not change.
- mark8ly merchants' own store inboxes do not change.
- Push notifications for new waiting chats on mobile (future follow-up).

## Decisions made during brainstorming

1. **Approach A chosen:** extend otto's cross-tenant `platform` API surface
   with a full inbox rather than (B) a per-tenant switcher over the existing
   admin surface or (C) funnelling every product into one `platform` tenant.
   Rationale: unified queue, zero per-product changes, preserves per-tenant
   isolation that future AI routing (tenant → RAG namespace) depends on.
2. ~~The customer-side bubble in tesserix-home admin stays ("keep both")~~
   **REVERSED 2026-07-25 (post-approval, user screenshot feedback):** the
   `OttoSupportChat` widget (customer-side "start a conversation" bubble) is
   **removed** from the tesserix-home admin layout. In the admin, otto is
   staff-side only — admins connect TO customers via the inbox; they do not
   open chats. The widget remains on tesserix-home's public marketing pages
   and in every product app.
3. **Added 2026-07-25 (same feedback): ticket escalation from a chat.**
   When staff cannot resolve a conversation in chat, they can create a
   support ticket from the conversation view, pre-filled from the thread
   (customer identity, product/tenant, intake reason, case id CS-…, and a
   transcript reference) into tesserix-home's existing platform tickets
   system (`/api/admin` tickets routes). Phase 3 scope — web admin first;
   the mobile inbox (Phase 4) gets the same action. The inbox must also
   surface the product (tenant badge) and the customer's intake problem
   prominently in the accept flow, so staff understand who/what before
   they take the chat (already part of the OttoInbox platform mode).

## 1. Otto backend (`slm-support-platform/services/otto`)

Extend the existing `/api/v1/platform/otto` group (today: `/stats` only),
reusing `AdminHandler` internals with tenant scope made optional (empty
tenant = all tenants):

```
GET  /api/v1/platform/otto/conversations            ?status=&tenant=&assignee=
GET  /api/v1/platform/otto/conversations/:id
GET  /api/v1/platform/otto/conversations/:id/messages
POST /api/v1/platform/otto/conversations/:id/accept
POST /api/v1/platform/otto/conversations/:id/messages
POST /api/v1/platform/otto/conversations/:id/close
POST /api/v1/platform/otto/ws-ticket
GET  /api/v1/platform/otto/ws                        (inbox stream, all tenants)
GET  /api/v1/platform/otto/conversations/:id/ws      (per-thread)
```

- **Auth:** `PlatformAuth` keeps its mandatory-secret rule (deny on empty
  secret — a cross-tenant surface must never fall open). Mutating endpoints
  and the inbox list additionally require `X-User-Id` (staff identity headers
  forwarded by the tesserix-home proxy: `X-User-Id`, `X-User-Email`,
  `X-User-Name`, `X-User-Role`) so accept/reply/close are attributed.
  Introduce this as a `PlatformStaff` middleware layered on `PlatformAuth`.
- **WebSockets:** mounted on a no-middleware group with short-TTL ticket auth,
  mirroring the existing admin WS pattern (Istio routes WS directly to otto,
  bypassing the Next.js proxy). Tickets minted by `POST /ws-ticket` carry a
  platform scope instead of a tenant/store scope.
- **Filter data:** the tenant filter chips reuse the existing
  `PlatformStats.by_tenant` rollup — no new endpoint.
- **Audit/availability:** reuse existing repos; audit entries record the
  staff identity and the conversation's tenant.

**Verify at planning time:**
- Hub subscription keying — the cross-tenant inbox stream needs either a
  wildcard subscription or per-tenant fan-in in `internal/hub`.
- Mongo index to support the global cross-tenant list
  (`status` + last-activity sort without a tenant prefix).
- Ticket signer scope encoding (must not allow a platform ticket to be
  replayed against tenant-scoped admin WS endpoints, or vice versa).

## 2. Widget (`design-system/packages/otto-widget` → 0.6.0)

`OttoInbox` gains a platform mode:

- New props: `tenantLabels?: Record<string, string>` (id → friendly product
  name) and a flag enabling cross-tenant UI (product badge per row, tenant
  filter chips, product name in the thread header).
- REST/WS request-response shapes are identical to the tenant-scoped admin
  surface (conversation documents already carry `tenant_id`), so the client
  layer changes are minimal — `apiBaseUrl` points at the platform proxy.
- Default (single-tenant) behaviour is unchanged for mark8ly admin.
- Publish `@tesserix/otto-widget@0.6.0` to GitHub Packages via the
  design-system repo's existing release flow.

## 3. tesserix-home admin web (`tesserix-home/apps/web`)

- **New proxy** `app/api/admin/otto/[...path]/route.ts` → otto's platform
  surface. Gated by the same admin session check the other `/api/admin/*`
  routes use; injects `X-Internal-Auth` (existing `OTTO_INTERNAL_AUTH` env —
  no new secrets) + staff identity headers from the session. Copies the
  existing `/api/otto` proxy's hardening: path-traversal segment guard,
  host/prefix pinning, 64KB body cap, 10s upstream timeout, 5xx body
  suppression.
- **New page** `/admin/support/live-chat` mounting `OttoInbox` in platform
  mode, plus a sidebar link alongside the existing support analytics entry.
- **Remove** the floating `OttoSupportChat` bubble from the admin layout
  (decision 2, reversed) — the admin is a staff-side surface only. The
  `/admin/analytics/support` page is untouched.
- **Ticket escalation** (decision 3): a "Create ticket" action on the
  conversation view that opens tesserix-home's existing ticket-creation
  flow pre-filled from the thread (customer, tenant/product label, reason,
  case id, transcript reference). Exact ticket-schema mapping is settled
  during Phase 3 planning against the existing `/api/admin` tickets routes.

## 4. tesserix-home admin mobile (`tesserix-home/apps/mobile`)

Native Expo screens — no WebView:

- **Inbox list**: waiting / active / closed tabs, product filter, unread and
  per-tenant counts.
- **Thread screen**: message history, accept, reply, close.
- **Data path:** the existing mobile gateway pattern — bearer-token calls via
  `plat.*` to the web app's new `/api/admin/otto/*` routes (the proxy must
  accept the mobile bearer the same way other `/api/admin/*` routes do —
  verify the shared auth helper covers it at planning time).
- **Real-time:** port the mark8ly `packages/mobile-shared/support` patterns
  (WS event parser, WebSocket → SSE → polling fallback, reconnect on
  app-state change) to a staff-side hook. WS connects directly to otto via
  the public host using the ws-ticket flow; polling fallback guarantees the
  inbox works even if the WS path misbehaves. Code is copied/adapted into
  tesserix-home (the kit lives in the mark8ly repo; promoting it to a shared
  package is a later refactor, not part of this work).

## 5. Infra (`tesserix-k8s`)

- One VirtualService change on the tesserix-home host: route
  `/api/v1/platform/otto/*` to the `support-platform-otto` service with a
  long WS timeout — mirroring the storefront otto WS routes that product
  domains already have.
- No new secrets, deployments, or charts. All changes flow through ArgoCD.

## Data flow (steady state)

1. Customer opens a chat in any product → conversation created in that
   product's tenant (unchanged).
2. Tesserix staff open the inbox (web or mobile) → platform proxy →
   `GET /platform/otto/conversations` across all tenants; inbox WS pushes new
   threads/updates live.
3. Staff accept → reply. Otto's hub delivers to the customer's storefront WS;
   the customer replies back the same way.
4. Close by staff, customer, or the 15-minute inactivity sweeper (unchanged).

## Error handling

- Proxy: 401 for missing/invalid admin session; 502 with suppressed body for
  upstream 5xx; 413 over body cap — same contract as the existing otto
  proxies.
- Otto platform endpoints: 401 on missing secret or staff identity; 404 for
  unknown conversation id (no tenant leak in error bodies).
- Mobile: WS drop → SSE → polling degradation; REST failures surface as
  toasts with retry, consistent with the rest of the app.

## Testing

- **otto (Go):** table tests for the platform handlers — cross-tenant list
  and filters, identity attribution on accept/reply/close, PlatformAuth
  denies on empty secret, ticket scope separation (platform ticket rejected
  on tenant admin WS and vice versa). Hub fan-in test for the all-tenant
  inbox stream.
- **widget:** type-check + lint (repo has no runtime test infra); manual
  smoke in mark8ly admin (single-tenant regression) and tesserix-home
  (platform mode).
- **tesserix-home web:** proxy route unit tests if the app's test setup
  allows; otherwise `pnpm build` + `tsc --noEmit` and manual verification.
- **mobile:** `tsc --noEmit` + simulator run against prod APIs.

## Rollout order

1. Otto backend → deploy (ArgoCD; image via slm-support-platform CI).
2. Widget 0.6.0 publish.
3. tesserix-home web + tesserix-k8s VirtualService → deploy.
4. Mobile screens → batched into the next tesserix-home mobile build.

Each step is independently shippable; the inbox page simply shows an empty
state until the backend is live.
