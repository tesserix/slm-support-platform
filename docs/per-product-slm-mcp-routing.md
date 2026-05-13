# Per-product SLM + MCP routing

## TL;DR

Every Tesserix product mounts the same `@tesserix/otto-widget` and points at
the same `support-platform-otto` service. What changes per product is the
`X-Tenant-ID` header. That header is the single switch the Otto service uses
to:

1. Select the right intake-reason whitelist for validation,
2. Route the conversation to the **per-product SLM** for the AI answer,
3. Bind that SLM to the **per-product MCP server** so it can fetch
   product-specific facts (orders for mark8ly, matches for fanzone,
   portfolios for stockpilot, charts for horoscope, etc.).

```
┌──────────────┐ X-Tenant-ID: fanzone        ┌─────────────────────────┐
│ fanzone-web  │ ───────────────────────▶    │   slm-router             │
└──────────────┘                              │  ┌────────────────────┐ │
┌──────────────┐ X-Tenant-ID: homechef       │  │ tenant ↦ SLM ↦ MCP │ │
│ homechef-web │ ───────────────────────▶    │  └────────────────────┘ │
└──────────────┘                              │       │           │     │
┌──────────────┐ X-Tenant-ID: stockpilot     │       ▼           ▼     │
│ stockpilot   │ ───────────────────────▶    │   slm-inference   MCP   │
└──────────────┘                              │   (per-tenant     pool  │
       …                                      │    model snapshot)      │
                                              └─────────────────────────┘
```

## Why per-product SLM

A single shared model trained on mark8ly's e-commerce voice answers a
horoscope question with order-tracking metaphors. The fix isn't smarter
prompting — it's a model whose **training data and retrieval index are
product-specific**.

- **mark8ly** — product catalogue, order schemas, return policy, payment
  flows.
- **fanzone** — cricket terminology, IPL schedule, points/leaderboard rules,
  prediction mechanics.
- **homechef** — chef profiles, delivery SLAs, refund policy, food-safety
  FAQs.
- **stockpilot** — broker integration docs, portfolio analytics, AI-agent
  trace explanations, billing tiers.
- **gameverse** — per-game rules, matchmaking explanations, room/lobby
  troubleshooting.
- **horoscope** — astrology traditions (Western / Vedic / Mian Xiang),
  chart components, "for entertainment only" disclaimers.
- **scrapper** — platform scraping limits, AI analysis tuning, publishing
  pipelines, OAuth token rotation.

## Why MCP

The SLM is the synthesiser. The **MCP server** is the tool layer that
gives it product-specific facts at inference time:

- mark8ly MCP: `lookup_order`, `lookup_return`, `list_payment_methods`.
- fanzone MCP: `get_match_details`, `get_user_points`, `list_predictions`.
- homechef MCP: `get_order_status`, `get_chef_availability`, `track_delivery`.
- stockpilot MCP: `get_portfolio`, `get_alpaca_account_status`,
  `get_agent_trace`.
- gameverse MCP: `get_room_state`, `get_ratings`, `get_match_history`.
- horoscope MCP: `get_chart`, `get_transits`, `get_palm_reading`.
- scrapper MCP: `get_scrape_job`, `get_publishing_pipeline_status`,
  `list_connected_accounts`.

The MCP tools are versioned, side-effect-free, and tenant-scoped — a
fanzone conversation cannot ever invoke a mark8ly tool.

## Frontend contract (in place now — v0.3.0 of `@tesserix/otto-widget`)

Every product wrapper passes three product-specific props:

```ts
import { OttoWidget, type ReasonOption } from "@tesserix/otto-widget";

const FANZONE_REASONS: readonly ReasonOption[] = [
  // `requiresStatus: false` is the quick-ask path — the "current
  // status / one-line summary" field is hidden and the backend skips
  // its check. Every product keeps a `general_question` entry at the
  // top so a fan can fire off a one-liner without a second form input.
  { value: "general_question", label: "Ask a quick question", requiresStatus: false },
  { value: "account_issue", label: "Account / login issue" },
  { value: "points_question", label: "Points or leaderboard question" },
  // …
];

<OttoWidget
  apiBaseUrl="/api/otto"
  tenantId="fanzone"
  reasons={FANZONE_REASONS}
  // Per-product example text. Never use the marketplace default
  // ("Order #2041 arrived damaged") in a non-marketplace product.
  statusPlaceholder="e.g. Points not updating after IPL #2042"
  // …customerName, customerEmail, productName
/>
```

The widget forwards `tenantId` as `X-Tenant-ID` on every Otto REST call
(see `packages/otto-widget/src/api.ts`). The reasons are sent in the
intake body and end up on the conversation document for the SLM to use
as routing context.

### Quick-ask reason (`general_question`)

Every tenant whitelists `general_question` and lists it as the first
reason. It's the one path where:

- the status field is hidden in the widget,
- `StatusRequiredFor(tenant, reason)` returns false on the backend so
  the storefront handler accepts an empty `status_info`,
- DOB is never asked (no `requiresDob: true`).

The conversation still carries `tenant_id`, so the SLM and MCP routing
behave the same as any other reason — only the intake gates relax.

### Tenant IDs

| Product | tenantId |
|---|---|
| Marketplace (mark8ly) | `mark8ly` |
| FanZone Battle Ground | `fanzone` |
| HomeChef (`fe3dr.com`) | `homechef` |
| StockPilot | `stockpilot` |
| GameVerse | `gameverse` |
| Tesserix Horoscope | `horoscope` |
| Social Media Scrapper | `scrapper` |

## Backend contract — current state

### Shipped (live in prod)

- **Tenant header trust.** The Otto service reads `X-Tenant-ID` from
  the proxy layer (Istio gateway → Next.js `/api/otto` proxy → Otto).
  Every conversation is created with `tenant_id` on the document — see
  `services/otto/internal/conversation/model.go`.
- **Per-tenant reason whitelist + DOB rules + status-required rules.**
  `TenantReasons` in `model.go` is the single source of truth.
  `IsReasonAllowed`, `DOBRequiredFor`, and `StatusRequiredFor`
  enforce the contract; unknown tenants fall back to mark8ly. Each
  tenant lists `general_question` so the quick-ask path is universal.
- **slm-router with per-tenant routing.** `slm-router` subscribes to
  the otto MongoDB change stream (single-node `rs0`, headless service +
  postStart `rs.initiate()`), looks up the tenant's prompt + MCP +
  embedder config, and posts the assistant reply back.
- **One MCP per product namespace.** Same `mcp-gateway` image is
  deployed into each product namespace; `MCP_TENANT` selects the tool
  set. Service DNS is `<tenant>-mcp.<tenant>.svc.cluster.local:8765`.
  Currently live: mark8ly, fanzone, homechef, gameverse, stockpilot,
  horoscope — image pinned to `main-f8789c4` because GAR pull-through
  caches by digest, so a floating `main` tag silently goes stale.
- **JSON-RPC 2.0 over plain POST at `/mcp`.** The slm-router doesn't
  speak FastMCP streamable HTTP (session ids, SSE, 307 redirects), so
  the mcp-gateway exposes a plain JSON-RPC handler that implements
  `initialize`, `tools/list`, and `tools/call`. FastMCP streamable can
  be mounted later at `/streamable` for a more capable client.
- **Cross-namespace NetworkPolicy.** `support-platform` is whitelisted
  in each product namespace's `allow-<product>-ingress` policy (see
  `tesserix-k8s/charts/thirdparty/istio-config/templates/network-policies.yaml`).
  Without that whitelist, slm-router → `<tenant>-mcp` traffic gets
  RST by kube-proxy at the destination side and tool discovery fails
  silently — the router falls back to "no tools" instead of erroring.
- **pgvector RAG per tenant.** Each tenant's knowledge base lands in
  `chunks.tenant_id = '<tenant>'` rows of the shared CNPG
  `support-platform-postgres` database; retrieval is filtered by
  `tenant_id` so a fanzone conversation never sees a mark8ly chunk.
  Embeddings come from TEI (`{"inputs": [...]}` request shape, bare
  `[[float]]` response).
- **Nightly export ↔ fine-tune feedback.** Closed conversations are
  exported per-tenant to `gs://tesseract-prod-otto-exports-in/<tenant>/<YYYY-MM-DD>.jsonl`;
  the LoRA training pipeline picks up only the matching tenant's
  shard, so cross-contamination is structurally impossible.

### Still to build

- **Real MCP tool implementations.** Current tools return stub JSON
  with `"_stub": true`; each needs to call the matching product's
  backend (order-service, match-service, portfolio-service, etc.).
- **Per-tenant LoRA fine-tunes.** Shared qwen2.5-1.5b base + per-tenant
  4-bit QLoRA adapter; needs ≥1k resolved conversations + GPU time.
  Until then every tenant runs on the shared base model + per-tenant
  system prompt.

### Acceptance test for end-to-end routing

```bash
# Fanzone — quick-ask path. No status_info required because the reason
# is general_question.
curl -sX POST https://fanzonebattleground.com/api/otto/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-ID: fanzone' \
  -d '{"reason":"general_question","message":"How do I check my points?"}'
# expect 201 + a conversation that gets an AI reply from the fanzone
# system prompt + fanzone-mcp tool calls + fanzone-namespaced RAG.

# Stockpilot — structured intake path.
curl -sX POST https://stockpilot.tesserix.app/api/otto/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-ID: stockpilot' \
  -d '{"reason":"broker_connection","status_info":"alpaca handshake timing out","message":"My Alpaca account is not syncing"}'
# expect 201 + reply that calls stockpilot-mcp's broker tools.
```

The widget side of the contract is deployed at v0.3.0; the backend
side (per-tenant whitelist, per-tenant MCP, RAG, export pipeline) is
shipped. Real tool implementations and per-tenant LoRA adapters are
the remaining work.
