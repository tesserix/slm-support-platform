# 06 — GKE Deployment Plan (CPU-only path)

Grounded in the actual state of `tesseract-prod-in-gke` (project `tesseracthub-480811`, region `asia-south1`) as of the planning date. Connect with `kubectl config use-context gke_tesseracthub-480811_asia-south1_tesseract-prod-in-gke` to verify any claim here against current reality.

> **Read [`08-otto-integration.md`](08-otto-integration.md) first.** Otto replaces `support-bff` / `support-router` / `support-orchestrator` (these three are no longer separate services). The component table below is updated to reflect that. Product scope is now **three** to start: mark8ly, fanzone, homechef.

## Cluster reality

| Thing | Current state | Implication |
|-------|---------------|-------------|
| Active nodes | 5× `optimized-v2` (`e2-standard-8`: 8 vCPU, 32 GiB), CPU only | CPU-only serving must fit these node specs |
| GPU node pool | `gpu-l4-spot` exists: `g2-standard-4` + 1× NVIDIA L4, spot, max 1 node, `asia-south1-b` | Available when we choose to enable; CPU-first per current decision |
| Service mesh | Istio ambient + per-namespace waypoints (`waypoint` Gateway per product ns) | Must label `support-platform` ns ambient + add 15008 NetworkPolicy |
| Edge gateway | `istio-ingress/tesseract-gateway` (`*.tesserix.app`), `istio-ingress/custom-domain-gateway` (customer domains) | Add `chat.tesserix.app` VirtualService under `tesseract-gateway`, no new gateway needed |
| Autoscaling | KEDA scaling 22 workloads, **including existing AI inference**: `devai/nemoclaw-inference` (0→1) and `stockpilot/fingpt-inference` | KEDA is the chosen scaler — reuse the pattern |
| Serverless | Knative Serving used by HomeChef (6 svcs), gameverse, fanzone (many svcs), horoscope | Use Knative for the BFF / router / MCP servers so they scale to zero |
| GitOps | ArgoCD with **215 apps** | Everything goes through ArgoCD; no manual `kubectl apply` (per CLAUDE.md) |
| DBs | CNPG Postgres per product (`mark8ly`, `fanzone`, `homechef`, `gameverse`, `stockpilot`, `scrapper`, ...) | Vector DB option: pgvector extension on per-product CNPG, or dedicated cluster in `support-platform` |
| Existing MCP precedent | `scrapper/scrapper-mcp` is already running (Python, port 8765, path `/mcp`, auth via `MCP_API_KEY` secret, image `scrapper-api:main-*`, externally `scrapper-mcp.tesserix.app`) | Same shape per product is the template |
| Product namespaces confirmed | `mark8ly`, `mark8ly-uat`, `fanzone`, `homechef`, `gameverse`, `stockpilot`, `scrapper`, `horoscope` | Five customer-facing products; widget integrates with each |

## Decision: do we need MCP?

**Yes.** Reasons:

- A working MCP server already exists in this cluster (`scrapper-mcp`) — the pattern, packaging, and routing are proven.
- MCP gives each product team **ownership** of their tool surface. The platform doesn't need to know what `homechef-mcp` exposes; it just lists, calls, and forwards results to the model.
- Onboarding a new product becomes: ship one MCP server + drop docs into a RAG namespace. Zero platform-side changes.
- The alternative — custom HTTP tool calls hand-rolled in the orchestrator — would re-invent MCP poorly and centralise the maintenance.

Each product gets one MCP server per the table below.

| Product | MCP server name | Tools to expose |
|---------|-----------------|-----------------|
| mark8ly | `mark8ly-mcp` | `find_product`, `check_inventory`, `track_order`, `vendor_status` |
| fanzone | `fanzone-mcp` | `match_status`, `redeem_reward`, `leaderboard`, `prediction_history` |
| homechef | `homechef-mcp` | `lookup_order`, `track_delivery`, `chef_payout_status`, `cancel_order` |
| gameverse | `gameverse-mcp` | `game_state`, `rejoin_match`, `rule_lookup`, `friend_list` |
| stockpilot | `stockpilot-mcp` | `portfolio_lookup`, `run_analysis`, `subscription_status`, `add_to_watchlist` |

Each MCP server runs in its **product's namespace** (not in `support-platform`), so it has direct in-mesh access to the product's existing APIs and DBs.

## Decision: do we need new gateways?

**No.** Use what's already there:

- **External edge.** Cloudflare → `istio-ingress/tesseract-gateway`. Add a single VirtualService for `chat.tesserix.app` → `support-bff.support-platform.svc.cluster.local` (or use the existing per-product custom-domain gateway if we want `chat.mark8ly.com`-style branding).
- **In-mesh routing.** Istio ambient handles east-west traffic. Per-namespace waypoint already gives L7 policy and observability for free.
- **The chat widget** itself is JavaScript embedded in each product frontend; it talks to `chat.tesserix.app` over HTTPS. No new ingress.

The only new ingress object is one VirtualService. Helm chart `tesserix-k8s/charts/apps/support-platform/` includes it.

## Decision: which tech to develop the SLM (CPU-only path)

| Layer | Choice | Why |
|-------|--------|-----|
| **Model** | Qwen2.5-1.5B-Instruct (int4 GGUF) for first pass; SmolLM2-1.7B and TinyLlama-1.1B as alternates | 1–2B params is the sweet spot for CPU. Bigger = unusable latency. Qwen2.5 has the best support/instruction quality at this size. |
| **Quantization** | int4 (Q4_K_M) GGUF | Halves memory, ~2× speed on CPU, quality loss <3% on instruction benchmarks |
| **Inference runtime** | `llama.cpp` server (or Ollama, which wraps it) | Best CPU performance available. AVX2/AVX-512 on the `e2-standard` family. vLLM CPU mode is much slower. |
| **Embedding model** | `bge-small-en-v1.5` (384-dim) | Smaller dim = less memory pressure; quality drop vs `bge-large` is real but acceptable for first launch |
| **Reranker** | `bge-reranker-base` int8 on CPU | Smaller variant; faster on CPU; only used for top-N candidates so per-query cost is bounded |
| **Vector DB** | `pgvector` in a dedicated CNPG cluster in `support-platform` namespace (not piggy-backed on a product's DB) | Keeps support-platform isolated from product DB scaling/load. Matches the existing per-product CNPG pattern. |
| **BFF / Orchestrator / Router** | Go + Gin | Matches every other Tesserix service. Reuses existing patterns (auth-bff, OpenFGA, Cloudflare tunnel) |
| **MCP servers** | Python (FastMCP) per product | Matches `scrapper-mcp` (Python). Each product team owns their MCP repo. |
| **Doc ingestion** | CronJob in `tesserix-k8s`, every 30 min | Mirrors `db-schema-bootstrap` pattern. Watches product doc repos, chunks, embeds, upserts. |
| **Frontend chat widget** | React component published from `@tesserix/web` package | Already the shared frontend package; widget mounts in each product's app shell |
| **Secrets** | GCP Secret Manager via External Secrets Operator | Standard Tesserix pattern; no in-cluster Kubernetes Secrets for app credentials |

### CPU performance expectations (be honest)

On a single `e2-standard-8` pod, Qwen2.5-1.5B int4 via llama.cpp:

- **Throughput:** ~8–15 tokens/second
- **Time to first token:** ~200–400 ms for short contexts
- **Total response:** ~3–8 seconds for a typical 60-token support reply
- **Cost:** ~$0.02 per node-hour for the slice we use → roughly $0.0005 per ticket at moderate load

This is 5–10× slower than GPU, *and that's fine for v1*. Customers expect "the bot is thinking" for a couple of seconds. We'll measure real latency and decide whether GPU is worth the cost only after we have traffic.

## Decision: how to scale

Two patterns, depending on the workload's nature.

### Pattern A — Knative Serving (scale to zero)
For: `support-bff`, `support-router`, `support-orchestrator`, every `*-mcp` server.
Why: most of the time these idle. Knative gives free 0→N scaling with cold-start in ~2s. Matches HomeChef, gameverse, fanzone, horoscope.

### Pattern B — KEDA (deployment-based, queue-aware)
For: `slm-inference` (llama.cpp pods), `reranker`.
Why: cold start of a quantized model is 5–15s (model load), not Knative-friendly. KEDA scales based on **pending HTTP request count** in a Redis queue or directly via the Prometheus metric. Min replicas = 1 (to absorb the first request), max replicas tuned per product. Same shape as `devai/nemoclaw-inference`.

```yaml
# sketch
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata:
  name: slm-inference
  namespace: support-platform
spec:
  scaleTargetRef:
    name: slm-inference
  minReplicaCount: 1
  maxReplicaCount: 3
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://prometheus.monitoring.svc.cluster.local:9090
      query: sum(rate(slm_request_queue_depth[1m]))
      threshold: "5"
```

Stateful pieces (pgvector, Postgres) don't autoscale on the data path. Reads can fan out via CNPG replicas if needed; we don't need that at launch.

## Component map and sizing

| Component | Namespace | Type | Scaling | Replicas (min→max) | Per-pod | Notes |
|-----------|-----------|------|---------|--------------------|---------|-------|
| `otto` (moved from mark8ly) | `support-platform` | Deployment | KEDA on WS conn | 1→3 | 200m / 512 Mi | Go server, WebSocket, multi-tenant via X-Tenant-Id |
| `support-mongo` | `support-platform` | StatefulSet | none | 1+1 replica | reuse Mongo chart | Otto's persistent store (`otto` DB) |
| `slm-router` (gateway) | `support-platform` | Deployment | KEDA on Mongo change-stream lag | 1→3 | 300m / 512 Mi | Watches Otto's Mongo, routes tenant → RAG → SLM → MCP |
| `slm-inference` | `support-platform` | Deployment + KEDA | Prom queue | 1→3 | 2 CPU / 6 Gi | llama.cpp server, int4 GGUF (Qwen2.5-1.5B) |
| `reranker` | `support-platform` | Deployment + KEDA | Prom queue | 0→2 | 500m / 2 Gi | bge-reranker-base int8 |
| `embedder` | `support-platform` | Deployment + KEDA | Prom queue | 0→2 | 500m / 1 Gi | bge-small-en, cacheable |
| `support-postgres` (CNPG) | `support-platform` | StatefulSet | none | 1+1 replica | reuse CNPG defaults | pgvector enabled, per-product RAG namespaces |
| `doc-ingestion` | `support-platform` | CronJob | n/a | every 30 min | 500m / 1 Gi | Markdown → chunks → embeds → upserts |
| `mark8ly-mcp` | `mark8ly` | Knative | request | 0→2 | 100m / 256 Mi | tools: find_product, check_inventory, track_order |
| `fanzone-mcp` | `fanzone` | Knative | request | 0→2 | 100m / 256 Mi | tools: match_status, redeem_reward, leaderboard |
| `homechef-mcp` | `homechef` | Knative | request | 0→2 | 100m / 256 Mi | tools: lookup_order, track_delivery, chef_payout_status |

Future onboarding (same pattern, not in v1 scope): `gameverse-mcp`, `stockpilot-mcp` once mark8ly + fanzone + homechef are stable.

CPU requests only — no CPU limits (per the Tesserix convention).

## Deployment topology

### New namespace: `support-platform`
- Labels: `istio.io/dataplane-mode=ambient`, `kubernetes.io/metadata.name=support-platform`
- NetworkPolicy: explicit allow TCP 15008 to/from the cluster pod CIDR (ambient HBONE — the silent-failure trap from the prior auth outage)
- ExternalSecret: pulls `support-platform-hf-token` (HuggingFace), `support-platform-mcp-keys` from GCP Secret Manager

### Helm chart
- Path: `tesserix-k8s/charts/apps/support-platform/`
- Subcharts or templates for: `slm-inference`, `embedder`, `reranker`, `support-bff`, `support-router`, `support-orchestrator`, `support-postgres` (CNPG cluster), `doc-ingestion-cronjob`, `network-policy`

### ArgoCD app
- Path: `tesserix-k8s/argocd/prod/apps/support-platform/`
- App-of-apps under existing pattern
- Auto-sync, self-heal off until first prod check (then on)

### MCP servers per product
- Each lives in **product's** chart: `tesserix-k8s/charts/apps/<product>/templates/<product>-mcp/` (or as a separate subchart)
- Same image-build pattern as `scrapper-mcp` (Python, FastMCP, Dockerfile, GH Actions → GHCR)
- ExternalSecret pulls `<product>-mcp-api-key`

### Image registry
- Same as everything else: `asia-south1-docker.pkg.dev/tesseracthub-480811/ghcr-remote/tesserix/slm-support-platform/<image>`

### Edge
- Single VirtualService under `istio-ingress/tesseract-gateway` for `chat.tesserix.app` → `support-bff.support-platform.svc.cluster.local`
- Cloudflare DNS record CNAME → existing ingress IP (same pattern as `argocd.tesserix.app`)

## How this maps onto the existing phase plan

The original Phase 2A doc assumed GPU + vLLM. The CPU-only decision swaps:

| Phase 2A step | Original (GPU) | CPU-only revision |
|---------------|----------------|-------------------|
| 1. Base model | Phi-3-mini (3.8B) | Qwen2.5-1.5B / SmolLM2-1.7B |
| 3. Fine-tune | LoRA on bf16 | LoRA on bf16 → quantize to int4 GGUF *after* tuning |
| 4. KV cache | vLLM paged attention | llama.cpp built-in (KV cache is universal) |
| 5. Quantization | AWQ int4 | GGUF Q4_K_M (better CPU-tuned) |
| 6. Batching | vLLM continuous batching | llama.cpp parallel slots (smaller wins, but real) |
| 7. FlashAttention | Required | Not applicable on CPU |
| 8. Speculative decoding | Optional | Skip — llama.cpp speculative is fiddly on CPU |
| 9. Serving | vLLM on L4 GPU | llama.cpp server on `optimized-v2` CPU pool |
| 10. Benchmark | GPU latency/throughput | CPU latency/throughput, real-traffic shaped |

The Phase 2B retrieval doc doesn't change — RAG / chunking / indexing / reranking are CPU-friendly either way.

## When to revisit GPU

After we have real traffic on the CPU stack and one of these becomes true:

- p95 response latency >10s and customer satisfaction is suffering
- Throughput requirement exceeds what 3× CPU pods can serve (~30 simultaneous chats)
- We want to move to Phi-3-mini or larger for quality reasons

Then enable `gpu-l4-spot` for `slm-inference` and switch to vLLM + AWQ int4. The Helm chart should be structured so this is a values change, not a redesign.

## Done condition for the GKE rollout

- `support-platform` namespace exists, ambient-labeled, with NetworkPolicy
- `chat.tesserix.app` resolves through tesseract-gateway
- All five products have an MCP server in their namespace
- Doc-ingestion CronJob is upserting docs into `support-postgres` pgvector
- Chat widget mounted in at least one product (suggest `tesserix-blog` or `horoscope` as low-stakes pilot)
- KEDA scaler keeps `slm-inference` at 1 replica idle, scales up under load
- An eval set per product runs in CI and gates retrieval regressions

## What to commit where

| Repo | Adds |
|------|------|
| `slm-support-platform` (this repo) | Application code: BFF, router, orchestrator, slm-inference Dockerfile, doc-ingestion script, eval harness |
| `tesserix-k8s` | Helm chart `charts/apps/support-platform/`, ArgoCD app `argocd/prod/apps/support-platform/`, ExternalSecrets for support-platform |
| Each product repo | One subchart `charts/<product>-mcp/` or a templates dir for the MCP server + its image build |
| `@tesserix/web` (design-system) | Chat widget React component |
