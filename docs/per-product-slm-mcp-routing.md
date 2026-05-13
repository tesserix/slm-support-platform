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

## Frontend contract (in place now — v0.2.0 of `@tesserix/otto-widget`)

Every product wrapper passes two product-specific props:

```ts
import { OttoWidget, type ReasonOption } from "@tesserix/otto-widget";

const FANZONE_REASONS: readonly ReasonOption[] = [
  { value: "account_issue", label: "Account / login issue" },
  { value: "points_question", label: "Points or leaderboard question" },
  // …
];

<OttoWidget
  apiBaseUrl="/api/otto"
  tenantId="fanzone"
  reasons={FANZONE_REASONS}
  // …customerName, customerEmail, productName
/>
```

The widget forwards `tenantId` as `X-Tenant-ID` on every Otto REST call
(see `packages/otto-widget/src/api.ts`). The reasons are sent in the
intake body and end up on the conversation document for the SLM to use
as routing context.

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

## Backend contract (what exists, what still needs to be built)

### Done

- The Otto service trusts the `X-Tenant-ID` header from the proxy layer
  (Istio gateway → Next.js `/api/otto` proxy → Otto). Every conversation
  is created with `tenant_id` on the document — see
  `services/otto/internal/conversation/model.go`.
- The intake validator only enforces `reason != ""`, `status_info != ""`,
  and DOB-when-required. It accepts any reason string, so per-product
  reasons from the widget are not rejected.

### To do (Phase-2 work, tracked under `slm-support-platform/phase2-optimizations`)

- **Tenant-scoped reason whitelist.** Replace the hardcoded
  `ReasonOrderIssue / ReasonReturn / …` consts in
  `conversation/model.go` with a per-tenant lookup. Source of truth is
  this doc; backend mirrors the same labels.
- **SLM selection.** Today the slm-router uses a single inference
  deployment. Add `tenant_id`-aware routing so each tenant points at its
  own LoRA-on-shared-base or its own model snapshot. The router contract
  is described in `slm-router/internal/router/route.go`.
- **MCP server per tenant.** Each product gets its own MCP server (see
  `scrapper-mcp` as the reference pattern — it's already deployed in
  the `scrapper` namespace). The Otto agent fetches the
  `tenant_id`-matched MCP endpoint from the slm-router and binds tools
  scoped to that tenant.
- **RAG index per tenant.** Each tenant's knowledge base (product docs,
  FAQs, support transcripts) lands in its own pgvector collection.
  Naming: `kb_<tenant_id>`. The retrieval step runs against the
  collection chosen by `tenant_id`; never across tenants.
- **Conversation export ↔ training feedback.** Closed cases for tenant
  X are exported nightly into the tenant-X fine-tune dataset only —
  never mixed across products. Cross-contamination is the single most
  expensive thing to roll back later.

### Acceptance test for end-to-end routing

Once the backend work above lands, this should hold for every product:

```bash
# Fanzone tenant
curl -sX POST https://fanzonebattleground.com/api/otto/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-ID: fanzone' \
  -d '{"reason":"account_issue","status_info":"can\'t log in"}' \
  | jq '.routing'
# expect: { "tenant": "fanzone", "slm_model": "otto-fanzone-…",
#           "mcp_endpoint": "https://fanzone-mcp.support-platform…" }

# Stockpilot tenant on the same Otto deployment
curl -sX POST https://stockpilot.tesserix.app/api/otto/conversations \
  -H 'Content-Type: application/json' \
  -H 'X-Tenant-ID: stockpilot' \
  -d '{"reason":"broker_connection","status_info":"alpaca handshake timing out"}' \
  | jq '.routing'
# expect: { "tenant": "stockpilot", "slm_model": "otto-stockpilot-…",
#           "mcp_endpoint": "https://stockpilot-mcp.support-platform…" }
```

The widget side of the contract is already deployed (v0.2.0). The
backend side is the next milestone for slm-support-platform.
