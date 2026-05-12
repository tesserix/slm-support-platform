# 08 — Otto Integration & Migration

This doc supersedes the parts of `02-end-state-architecture.md`, `06-gke-deployment-plan.md`, and `07-execution-roadmap.md` that assumed we were building a chat front end and BFF from scratch. **We're not.** Otto already exists in `mark8ly/services/otto` and was designed to be reused.

## What Otto already is

A generic, multi-tenant, real-time customer-↔-staff chat service. Read its own [README](../../../mark8ly/services/otto/README.md):

> *Tenant-isolated, real-time customer↔staff chat service backed by MongoDB. Designed to be reused by any tesserix product — its API surface is deliberately generic and its only mark8ly-specific assumption is that incoming requests carry `X-Tenant-Id` + `X-Store-Id` headers from an upstream proxy.*

What's already done:

- **Backend** (`mark8ly/services/otto`, Go, port 8089, MongoDB-backed)
- **Widget package** (`mark8ly/packages/otto-widget`, React, `<OttoWidget />` for customers + `<OttoInbox />` for staff)
- **Auth** — signed HttpOnly `otto_session` cookie + OTP email via SendGrid, internal-auth shared secret between Otto and the Next.js proxies
- **Tenancy** — every Mongo read/write filters by `tenant_id` + `store_id`; cookies are scoped to the tenant
- **Real-time** — three WebSocket endpoints (customer thread, staff inbox, staff per-thread)
- **Lifecycle** — `pending → active → closed` conversation states, sweeper for stale threads, audit log
- **Surface** — full storefront + admin API for conversations and messages

The mark8ly README literally says:

> *Nothing in the service code references mark8ly. Reuse: spin up an instance of this binary pointed at its own MongoDB, proxy `/api/v1/storefront/otto/*` and `/api/v1/admin/otto/*` through any Next.js app with the appropriate header injection, mount the widgets.*

## Three decisions that shape what comes next

| Decision | Choice | Implication |
|----------|--------|-------------|
| Where Otto lives | **Move into `slm-support-platform` as canonical** | mark8ly stops shipping its own image and consumes this one. One image, three product tenants. |
| AI-vs-human model | **AI first, escalate to human** | Every new customer message goes through `slm-router` and the SLM. Escalates to staff inbox on low confidence, tool failure, or explicit customer request. |
| Topology | **One shared Otto deployment in `support-platform` namespace** | Single Otto + single support-mongo serves mark8ly, fanzone, homechef via `tenant_id`. |

## Revised architecture

Otto stops being the only chat service; it becomes the chat *front-of-house*. A new `slm-router` is the gateway behind it.

```
Customer
   │
   ▼  product frontend (mark8ly.com / fanzonebattleground.com / fe3dr.com)
   │  mounts @tesserix/otto-widget
   │
   ▼  /api/v1/storefront/otto/* (Next.js proxy on the product site)
   │  injects X-Tenant-Id, X-Store-Id, X-Internal-Auth
   │
   ▼  otto (support-platform ns)
   │  • persists conversation + messages to support-mongo
   │  • for each new customer message:
   │      emits "ai-turn" event on its internal hub
   │
   ▼  slm-router (support-platform ns) — THE GATEWAY
   │  • subscribes to "ai-turn" events
   │  • looks up tenant_id → product config (system prompt, RAG namespace, MCP servers)
   │  • calls RAG retriever for context
   │  • composes prompt and calls slm-inference
   │  • on tool calls, invokes the right product's MCP server
   │  • posts the AI reply back to Otto as a message of role="assistant"
   │  • on low confidence or tool failure: escalate (sets conversation needs_human=true)
   │
   ▼  Otto streams the AI message back over the customer's WebSocket
   │  Customer sees the reply
   │
   │  If escalated: conversation appears in staff inbox; staff takes over
```

The "gateway to direct to the right SLM" is `slm-router`. It owns:
- The mapping from `tenant_id` → product config
- The RAG namespace selection
- The MCP server selection
- The escalation policy

## Why this is *much* better than building from scratch

| Thing we were planning to build | Already done by Otto |
|---------------------------------|----------------------|
| support-bff with session, SSE streaming | Otto + WebSocket |
| Chat widget React component | `@repo/otto-widget` |
| Conversation persistence | MongoDB collections in Otto |
| Tenant isolation | `tenant_id` + `store_id` everywhere |
| Customer auth (cookie + OTP) | SendGrid + cookies in Otto |
| Staff inbox + assignment | Admin API + WS in Otto |
| Audit trail | Conversation audit log in Otto |

What we still build:

- **`slm-router`** — the gateway (new)
- **`slm-inference`** — model serving (planned, llama.cpp + Qwen2.5-1.5B)
- **`embedder`**, **`reranker`**, **`support-postgres`** (pgvector) — RAG stack (planned)
- **MCP servers** per product — `mark8ly-mcp`, `fanzone-mcp`, `homechef-mcp` (planned)
- **Doc-ingestion CronJob** (planned)
- **Hub event for "ai-turn"** — small Otto change to emit an event when a customer message arrives without a staff assignee

## Repo layout change

Otto moves *into* this repo:

```
slm-support-platform/
├── services/
│   ├── otto/                    moved from mark8ly/services/otto
│   ├── slm-router/              NEW
│   ├── slm-inference/           NEW (llama.cpp Docker)
│   ├── embedder/                NEW (bge-small)
│   ├── reranker/                NEW (bge-reranker-base)
│   └── doc-ingestion/           NEW (CronJob script)
├── packages/
│   └── otto-widget/             moved from mark8ly/packages/otto-widget,
│                                renamed @tesserix/otto-widget
├── phase1-from-scratch/         (unchanged — learning track)
├── phase2-optimizations/        (unchanged — model + RAG optimization plans)
├── phase3-agentic-support/      (unchanged — placeholder, but Phase 3 work now means slm-router + MCP wiring)
└── docs/                        (the planning docs)
```

The mark8ly repo loses `services/otto`, `packages/otto-widget`, and its dedicated `mark8ly-otto` Helm chart. Its Next.js `/api/otto` proxies stay but point at the shared Otto in `support-platform.svc.cluster.local`.

## Multi-tenancy: how three products share one Otto

| Product | `X-Tenant-Id` | Customer domain | `otto_session` cookie domain | RAG namespace | MCP server |
|---------|--------------|------------------|------------------------------|---------------|-----------|
| mark8ly | `mark8ly` | `mark8ly.com` | `.mark8ly.com` | `mark8ly` | `mark8ly-mcp` (mark8ly ns) |
| fanzone | `fanzone` | `fanzonebattleground.com` | `.fanzonebattleground.com` | `fanzone` | `fanzone-mcp` (fanzone ns) |
| homechef | `homechef` | `fe3dr.com` | `.fe3dr.com` | `homechef` | `homechef-mcp` (homechef ns) |

Otto already enforces tenant isolation in Mongo. We add a table in `slm-router` that maps `tenant_id` → `{system_prompt, rag_namespace, mcp_servers[], escalation_policy}`.

The cookie domain stays per-product so customers don't see cross-site cookies. The Next.js proxy on each product injects the right `X-Tenant-Id` based on the Host header.

## CORS and ingress

Otto already has `CORS_ALLOWED_ORIGINS`. Today it's `https://*.mark8ly.com`. After move, it becomes:

```
https://*.mark8ly.com,https://*.fanzonebattleground.com,https://*.fe3dr.com
```

No changes to Otto's code — just a values change in the Helm chart.

Otto sits in `support-platform` ns. Each product's Next.js proxy calls Otto over the in-cluster service DNS (`otto.support-platform.svc.cluster.local:8089`). No new VirtualService for Otto itself — the product frontends are the user-facing edge.

If we want a direct admin route (e.g., `inbox.tesserix.app`), add one VirtualService on the existing `tesseract-gateway`.

## MongoDB: new cluster in support-platform

Otto currently uses `mark8ly-mongodb` (in `mark8ly` ns). After move:

- New CNPG-managed Mongo or a fresh `mongodb` StatefulSet in `support-platform` ns: `support-mongo`
- DB name: `otto` (keep the existing schema; nothing about it is mark8ly-specific)
- Connection: `mongodb://support-mongo.support-platform.svc.cluster.local:27017`

**Data migration plan** (very small, since current mark8ly Otto is `0/1` per kubectl):
1. Stand up `support-mongo` empty
2. Use `mongodump` from `mark8ly-mongodb`'s `otto` DB → `mongorestore` into `support-mongo`
3. Run Otto in `support-platform` against `support-mongo`
4. Once cutover validated, scale `mark8ly-otto` Deployment to 0 and remove from `tesserix-k8s`

Since the current `mark8ly-otto` is `0/1` (not running), there might not be any production conversation data to lose. Confirm before deleting.

## The "ai-turn" event — minimal change to Otto

Today Otto's flow on a customer message: append to thread → notify staff inbox via hub.

After change: append to thread → notify hub of *two* event types:
- `staff.message_pending` (existing, drives staff inbox WS)
- `ai.turn_requested` (new, drives slm-router)

`slm-router` subscribes (via Mongo change stream or an internal Otto HTTP webhook — Mongo change stream is the cleaner choice and Otto already uses MongoDB so this is free).

`slm-router` posts the AI reply back as a message of `role="assistant"` (today Otto only knows `role="customer"` and `role="staff"`; we add `role="assistant"`). Otto's existing WS pipeline streams it to the customer like any other message.

The Otto code changes are small:
1. Add `role="assistant"` and corresponding constant
2. Add `needs_human: bool` field on conversation (set by slm-router when escalating)
3. (Optional) Emit a change-stream-friendly marker on customer messages so slm-router watches efficiently

## slm-router design

A small Go service. Responsibilities:

- **Watch** Otto's `conversations.messages` change stream for new customer messages
- **Skip** messages on conversations where `needs_human=true` (human already took over)
- **Resolve** the product config from `tenant_id`
- **Retrieve** RAG context: `embedder.Encode(query) → support-postgres.NearestK(namespace=tenant_id, k=10) → reranker.Score → top-5`
- **Plan** tool calls: load each product's MCP server's tool schemas, present them to the model
- **Call** slm-inference with the composed prompt
- **Execute** tool calls by routing to the right `*-mcp` server
- **Score confidence** (heuristic on the model output — short replies, hedging language, refusal patterns)
- **Decide** to post the reply or escalate (`needs_human=true` + post a soft "let me get a human" message)

Approximate config schema:

```yaml
products:
  mark8ly:
    system_prompt_file: prompts/mark8ly.md
    rag_namespace: mark8ly
    mcp_servers:
      - name: mark8ly-mcp
        url: http://mark8ly-mcp.mark8ly.svc.cluster.local:8765/mcp
        auth_secret: mark8ly-mcp-key
    escalation:
      confidence_threshold: 0.6
      keywords: ["refund", "lawyer", "complaint to ombudsman"]
  fanzone:
    system_prompt_file: prompts/fanzone.md
    rag_namespace: fanzone
    mcp_servers:
      - name: fanzone-mcp
        url: http://fanzone-mcp.fanzone.svc.cluster.local:8765/mcp
        auth_secret: fanzone-mcp-key
    escalation:
      confidence_threshold: 0.65
      keywords: ["refund", "chargeback"]
  homechef:
    system_prompt_file: prompts/homechef.md
    rag_namespace: homechef
    mcp_servers:
      - name: homechef-mcp
        url: http://homechef-mcp.homechef.svc.cluster.local:8765/mcp
        auth_secret: homechef-mcp-key
    escalation:
      confidence_threshold: 0.6
      keywords: ["food poisoning", "allergic reaction", "lawyer"]
```

Lives in `tesserix-k8s/charts/apps/support-platform/values.yaml` and is materialised as a ConfigMap mounted into slm-router.

## Migration & cutover plan

A coordinated change spanning mark8ly + slm-support-platform + tesserix-k8s. Sequence matters.

### Stage 1 — Stand up shared Otto in `support-platform` (no AI yet, no traffic yet)
1. Copy `services/otto` + `packages/otto-widget` into this repo (preserve git history with `git filter-repo`)
2. Rename package: `@repo/otto-widget` → `@tesserix/otto-widget`, publish to GHCR
3. Build new image: `slm-support-platform/otto:<sha>` (the binary is unchanged, just rebuilt from new path)
4. Helm chart in `tesserix-k8s/charts/apps/support-platform/templates/otto/`
5. ExternalSecret for `support-otto-secrets`
6. Stand up `support-mongo` (empty)
7. Deploy via ArgoCD. Run Otto with `replicas: 1`, no product proxies pointing at it yet.

### Stage 2 — Migrate Mongo data (if any) and switch mark8ly proxy
1. `mongodump --uri mongodb://mark8ly-mongodb/otto > otto-data.bson` (or skip if no data)
2. `mongorestore --uri mongodb://support-mongo --db otto otto-data.bson`
3. Update mark8ly's Next.js `/api/otto` proxy to point at `support-platform.otto:8089` instead of `mark8ly.otto:8089`
4. Update CORS_ALLOWED_ORIGINS in the new Otto to include `*.mark8ly.com`
5. Deploy mark8ly frontend, traffic now flows to shared Otto
6. Watch metrics: WebSocket connections, latency, errors. Roll back if anything breaks.

### Stage 3 — Remove old `mark8ly-otto`
1. Scale `mark8ly-otto` Deployment to 0
2. After 24h of stable shared Otto, remove `mark8ly-otto` from `tesserix-k8s`
3. Drop `services/otto` and `packages/otto-widget` from the mark8ly repo. mark8ly now consumes `@tesserix/otto-widget` from GHCR.

### Stage 4 — Add AI: deploy slm-inference, slm-router, RAG stack
1. Stand up `slm-inference` (llama.cpp + Qwen2.5-1.5B int4), `embedder`, `reranker`, `support-postgres` per Phase 2A + 2B plans
2. Deploy `slm-router` with mark8ly config only initially
3. Add `role="assistant"` to Otto code (small PR)
4. Add `needs_human` field on conversation
5. Smoke test internally — open a thread on mark8ly admin page, watch slm-router pick it up and respond

### Stage 5 — AI rollout per product
- **mark8ly** — enable AI in slm-router config for `tenant_id=mark8ly`. Customers see AI replies on `mark8ly.com`.
- **fanzone** — add Next.js `/api/otto` proxy on fanzone frontend, mount `@tesserix/otto-widget`, add `tenant_id=fanzone` to slm-router config, ship `fanzone-mcp`, ingest fanzone docs into RAG namespace.
- **homechef** — same as fanzone for `fe3dr.com`.

Each per-product onboarding takes one PR per repo (product repo for the proxy + widget mount, tesserix-k8s for the MCP server chart + slm-router config, slm-support-platform for the system prompt + eval set).

## Product scope reduction

The earlier docs assumed five products (mark8ly, fanzone, HomeChef, gameverse, stockpilot). The new scope is **three**: mark8ly, fanzone, homechef. gameverse and stockpilot become **future onboarding** following the same per-product checklist.

The architecture supports N products; we're just deferring two.

## What this means for the existing planning docs

| Doc | What needs updating |
|-----|---------------------|
| `02-end-state-architecture.md` | Replace "support-bff" with "Otto + slm-router"; reduce products to 3 |
| `06-gke-deployment-plan.md` | Remove `support-bff` / `support-router` / `support-orchestrator` as separate services; add `otto` + `support-mongo` + `slm-router`; reduce products to 3 |
| `07-execution-roadmap.md` | Workstream B now starts by moving Otto, not by scaffolding from zero; new workstream D = Otto migration |
| `diagrams/architecture.drawio` | Replace BFF with Otto; show slm-router as the gateway; reduce to 3 products |
| `diagrams/customer-flow.drawio` | Replace BFF lane with Otto + Mongo lanes; show slm-router between Otto and SLM |

I'll do these updates incrementally as we land each migration stage.

## Done condition for the Otto integration phase

- One Otto Deployment in `support-platform` ns, replicas ≥ 1, healthy
- `support-mongo` running with `otto` DB populated (even if empty)
- mark8ly frontend talks to shared Otto (proxy points there, customers see no change)
- Old `mark8ly-otto` Deployment removed
- `slm-router` watches Otto's Mongo and posts AI replies for `tenant_id=mark8ly`
- AI escalation works (low confidence → conversation moves to staff inbox)
- fanzone and homechef frontends mount the widget and proxy to Otto with their tenant ids

When all true, the platform is live for three products end-to-end.
