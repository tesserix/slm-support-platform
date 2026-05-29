# 09 — Latency Observability Plan

**Status:** proposed
**Owners:** slm-support-platform
**Depends on:** `06-gke-deployment-plan.md` (published latency targets)

---

## Why

We ship the Otto chat across 8 product tenants on a shared, CPU-only
Qwen2.5-1.5B (Q4_K_M, llama.cpp). The published latency targets in
[`06-gke-deployment-plan.md`](06-gke-deployment-plan.md) — TTFT
200–400 ms, throughput 8–15 tok/s, total 3–8 s for a ~60-token reply —
are **engineering targets**, not measurements. When a customer asks "how
slow is the self-hosted stack vs hitting Claude directly?" we currently
answer with estimates. The only number we have from production is the
client-observable wall-clock, which collapses every hop into one
opaque value and doesn't tell us where to spend optimisation budget.

This document specifies the histogram set we need to be able to answer
that question with `histogram_quantile(0.95, …)` instead of a guess.

---

## What we're already publishing

Today, only the istio sidecar / waypoint metrics are scraped on the
support-platform pods. Nothing in the application layer reports
duration histograms. Specifically:

| Service                    | `/metrics` exposed | Histograms today | Gap |
|----------------------------|--------------------|------------------|-----|
| `otto`                     | no                 | none             | end-to-end REST + WS broadcast latency |
| `slm-router`               | mentioned in `services/slm-router/internal/httpserver/server.go:8` as a TODO; not wired | none | full orchestration cost |
| `slm-inference` (llama.cpp)| llama.cpp `--metrics` flag emits a few prom counters; not scraped | partial (counters only) | per-request TTFT, decode rate, queue depth |
| `embedder` (TEI)           | TEI emits prom histograms on `/metrics` | yes, not scraped | — (just needs scrape) |
| `reranker` (TEI)           | same | yes, not scraped | — (just needs scrape) |
| `mcp-gateway`              | no                 | none             | per-tool round-trip |
| `cnpg/postgres` + `mongo`  | exporters available; not deployed | none             | pgvector query time, mongo read p95 |

So the work splits into "wire up what already emits" (TEI + llama.cpp +
CNPG postgres exporter) and "add Prom client + histograms to our Go
services" (otto, slm-router, mcp-gateway).

---

## Histogram catalogue

Every histogram below uses the **same bucket layout** so a Grafana
dashboard can stack them without renormalising:

```
buckets = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30]
```

(seconds; the upper bound covers the 30 s long-tail timeout we set on
inference calls).

All histograms carry a **`tenant`** label (`mark8ly` | `fanzone` |
`homechef` | `stockpilot` | `gameverse` | `horoscope` | `scrapper` |
`platform`). Don't add `conversation_id` — high-cardinality, useless
for aggregates. Per-conversation tracing belongs in OTel, not Prom.

### `otto` (Go, Gin, `prometheus/client_golang`)

`services/otto/internal/observability/metrics.go` *(new file)*:

```go
otto_http_request_duration_seconds{route, method, status, tenant}
  // every REST handler — middleware emits once per request

otto_ws_broadcast_duration_seconds{type, tenant}
  // hub.Broadcast latency: time from change-stream event arriving
  // to all subscribed clients flushed. type ∈ {message_created,
  // conversation_updated, conversation_closed}

otto_changestream_lag_seconds{collection, tenant}
  // mongo cluster_time of the document minus time when the watcher
  // handled it. Detects mongo replication lag pretending to be
  // realtime lag.

otto_ws_clients{tenant}        // gauge — connected WS clients
otto_active_conversations{tenant, status}  // gauge — pending/active/closed
```

Middleware mounts in `cmd/server/main.go` between
`httpserver.New` and the route registrations so every group inherits
it. Exempt `/health` and `/readyz`.

### `slm-router` (Go, the existing TODO is here)

`services/slm-router/internal/observability/metrics.go` *(new file)*:

```go
# end-to-end orchestrator wall-clock (per turn)
slm_router_orchestrate_duration_seconds{tenant, outcome, tools_called}
  // outcome ∈ {answered, escalated, timeout, error}
  // tools_called is a bool string label "true"/"false"

# the four big sub-steps of the orchestrator
slm_router_embed_duration_seconds{tenant}
slm_router_retrieve_duration_seconds{tenant, source}
  // source ∈ {pgvector, mongo_kb_fallback}; records K=50 lookup
slm_router_rerank_duration_seconds{tenant}
slm_router_inference_duration_seconds{tenant, call_index}
  // call_index ∈ {"1", "2"} — 2 means a tool-calling round happened

# token accounting
slm_router_prompt_tokens{tenant, call_index}     # histogram, buckets ~ 256..8192
slm_router_completion_tokens{tenant, call_index} # histogram, buckets ~ 8..512
slm_router_inference_tokens_per_second{tenant}   # histogram, buckets ~ 1..30

# RAG quality signals
slm_router_retrieved_chunks{tenant}              # histogram, count of top-K hits actually used
slm_router_rerank_dropped_total{tenant}          # counter — retrieved minus kept after rerank
```

`outcome=timeout` is critical — that's where the 90 s orchestrator
wall-clock catches a stuck llama.cpp slot. Today we'd find that only
in logs.

### `slm-inference` (llama.cpp)

llama.cpp already exposes `/metrics` when started with `--metrics`. We
just need to **enable scraping** and add it to our chart:

```yaml
# charts/apps/support-platform-slm-inference/values.yaml
service:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "8080"
    prometheus.io/path: "/metrics"
extraArgs:
  - "--metrics"
```

The metrics worth alerting on:

```
llamacpp_requests_processing             # gauge — slots currently busy
                                         # (we configure 4 slots; alert at ≥ 3 sustained)
llamacpp_requests_deferred               # gauge — queue depth
                                         # (alert at > 0 for 1 min — backpressure)
llamacpp_n_decode_total                  # counter — total tokens decoded
llamacpp_n_prompt_tokens_processed_total # counter — total prompt tokens
```

`n_decode_total / time` is the actual decode rate — compare to the 8–15
tok/s target. `requests_deferred` is the first thing that climbs when
we're capacity-bound; it's the cleanest signal for "time to add a
replica or move to GPU".

### `embedder` + `reranker` (TEI)

Already emit on `/metrics`. Just need:

```yaml
# both charts
service:
  annotations:
    prometheus.io/scrape: "true"
    prometheus.io/port: "80"
```

Key series TEI emits:

```
te_request_duration                      # histogram per request
te_batch_inference_duration              # histogram per batch
te_batch_concat_duration                 # histogram (queue + concat time)
te_request_failure_total                 # counter
```

`te_batch_inference_duration` is the one we'll quote against the
"retrieval dominates?" question — likely 20–80 ms p95 for the embedder
and 150–300 ms p95 for the reranker on CPU.

### `mcp-gateway` + per-product MCP apps

Add to each MCP app's tool handler:

```
mcp_tool_call_duration_seconds{tenant, tool, status}
  // status ∈ {ok, error, timeout}
```

Tools that hit external APIs (Razorpay, Cloudinary, etc.) are the
ones with multi-second tails. Without per-tool buckets we can't say
which integration is dragging p95.

### CNPG postgres (pgvector RAG store)

Deploy `cnpg-system`'s built-in postgres-exporter sidecar
(already supported, just toggle in the Cluster spec). Query
of interest:

```
pg_stat_statements_seconds_total{queryid="<top-k-cosine>", db="otto_rag"}
```

We can also tag the `EXPLAIN ANALYZE` of the cosine-distance lookup
on commit and store the plan in a Grafana annotation — that's
enough to know when ivfflat needs reindexing.

---

## SLOs

Once the histograms above are live for at least 7 days, target:

| Signal                                              | SLO p95    | Error budget |
|-----------------------------------------------------|------------|--------------|
| `otto_http_request_duration_seconds{route="/messages"}` (REST POST a new message) | **400 ms** | 1% / 30 d   |
| `otto_ws_broadcast_duration_seconds{type="message_created"}` | **150 ms** | 0.5% / 30 d |
| `otto_changestream_lag_seconds`                     | **250 ms** | 1% / 30 d   |
| `slm_router_orchestrate_duration_seconds{outcome="answered"}` | **8 s**   | 5% / 30 d   |
| `slm_router_embed_duration_seconds` + `_retrieve_` + `_rerank_` (sum of p95s) | **500 ms** | 1% / 30 d   |
| `slm_router_inference_duration_seconds{call_index="1"}` | **6 s**   | 5% / 30 d   |
| `llamacpp_requests_deferred`                        | **0**      | < 1 min / day above 0 |

The `slm_router_orchestrate` p95 is intentionally generous (8 s) because
it includes the CPU decode tail and is the honest "what does the user
feel" number. The component SLOs let us see whether a breach was a
retrieval problem (cheap to fix — index tuning, better embedder) or an
inference problem (only solvable with GPU / quantisation / model size).

---

## Dashboard layout (Grafana)

One stat row plus three breakdown rows, all filtered by `tenant`:

1. **Top row — current state.** Connected WS clients, active
   conversations, requests in-flight in llama.cpp, queue depth.
2. **End-to-end row.** `otto_http_request_duration_seconds` p50/p95/p99,
   `slm_router_orchestrate_duration_seconds` p50/p95/p99 split by
   `outcome`.
3. **Breakdown row.** Stacked area of embed / retrieve / rerank /
   inference p95 over time. **This is the graph the "where does the tax
   live?" question gets answered from.**
4. **Inference row.** llama.cpp tokens/sec (computed from
   `rate(llamacpp_n_decode_total[5m])`), prompt vs completion token
   distributions, deferred-requests gauge.

Export the dashboard JSON to `charts/thirdparty/grafana-dashboards/`
so it ships via ArgoCD like the rest.

---

## Implementation order

1. **TEI + llama.cpp scrape annotations** — zero code, just chart
   values. Lands within an ArgoCD sync. (~30 min wall-clock.)
2. **`slm-router` histograms** — biggest payoff because the
   orchestrator is where we'll spend optimisation time. Add the
   `observability` package, wrap each sub-step with a timer, deferred
   `.Observe()`. (~half a day.)
3. **`otto` middleware** — gin handler wrapping the histogram around
   the request lifecycle, plus the WS broadcast hook. (~half a day.)
4. **MCP tool histograms** — per-product (mark8ly first since it has
   the most real tool traffic). (~1 hour per product after pattern is
   set.)
5. **Grafana dashboard + recording rules** — compute and persist p95s
   as separate `:p95` series so alerts stay cheap. (~half a day.)
6. **PrometheusRule alerts** — `requests_deferred > 0 for 1m`,
   `orchestrate p95 > 12s for 5m`, `changestream_lag p95 > 1s for 2m`.

Total: **~2.5 dev-days end-to-end**, gated on whoever owns the
support-platform repo finding the slot. Step 1 is worth landing on
its own — TEI's histograms alone are enough to put numbers on the
"retrieval dominates?" question.

---

## What this lets us answer

Once steps 1–3 are live, the next time someone asks "where does Otto's
self-hosted tax actually live, p95?" the reply is two PromQL queries,
not estimates:

```promql
histogram_quantile(
  0.95,
  sum by (le) (rate(slm_router_inference_duration_seconds_bucket[1h]))
)

histogram_quantile(
  0.95,
  sum by (le) (
    rate(slm_router_embed_duration_seconds_bucket[1h]) +
    rate(slm_router_retrieve_duration_seconds_bucket[1h]) +
    rate(slm_router_rerank_duration_seconds_bucket[1h])
  )
)
```

Today we'd answer "~6 s vs ~300 ms" from a back-of-envelope. After
this lands we answer it from the actual cluster, per tenant, over any
time window.
