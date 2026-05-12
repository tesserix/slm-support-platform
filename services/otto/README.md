# otto — real-time support chat

Tenant-isolated, real-time customer↔staff chat service backed by MongoDB.
Designed to be reused by any tesserix product — its API surface is
deliberately generic and its only mark8ly-specific assumption is that
incoming requests carry `X-Tenant-Id` + `X-Store-Id` headers from an
upstream proxy.

## Surface

```
POST   /api/v1/storefront/otto/conversations              — customer starts thread (issues otto_session cookie)
GET    /api/v1/storefront/otto/conversations/:id          — customer fetches own thread
GET    /api/v1/storefront/otto/conversations/:id/messages — thread history
POST   /api/v1/storefront/otto/conversations/:id/messages — customer reply
POST   /api/v1/storefront/otto/conversations/:id/close    — customer closes
GET    /api/v1/storefront/otto/conversations/:id/ws       — customer WebSocket

GET    /api/v1/admin/otto/conversations                   — staff inbox (status/assignee filters)
GET    /api/v1/admin/otto/conversations/:id               — staff view
GET    /api/v1/admin/otto/conversations/:id/messages      — thread history
POST   /api/v1/admin/otto/conversations/:id/accept        — staff accepts
POST   /api/v1/admin/otto/conversations/:id/messages      — staff reply
POST   /api/v1/admin/otto/conversations/:id/close         — staff closes
GET    /api/v1/admin/otto/ws                              — staff inbox WS (new threads, updates)
GET    /api/v1/admin/otto/conversations/:id/ws            — staff per-thread WS
```

## Isolation guarantees

Every read and write filters by `tenant_id` + `store_id`. Customers prove
ownership of a thread with a signed HttpOnly `otto_session` cookie bound
to that scope; a cookie minted for store A can't open threads in store B.
Staff identity arrives via headers the admin proxy forwards after
validating `m8_session` against auth-bff. The service trusts those headers
provided the caller also presents `X-Internal-Auth` (a shared secret with
the Next.js proxies).

## Run locally

```
cp .env.example .env
# start mongo
docker run --rm -p 27017:27017 mongo:7
# run otto
go run ./cmd/server
```

Mongo indexes are created on boot — no separate migration step.

## Reuse

The handler stack, repositories, and hub are framework-agnostic Go
packages. A new host product only needs to:

1. Spin up an instance of this binary pointed at its own MongoDB.
2. Proxy `/api/v1/storefront/otto/*` and `/api/v1/admin/otto/*` through
   its Next.js app with the appropriate `X-Tenant-Id` / `X-Store-Id` /
   `X-Internal-Auth` injection.
3. Mount `@repo/otto-widget`'s `<OttoWidget />` on the customer side and
   `<OttoInbox />` on the staff side.

Nothing in the service code references mark8ly.
