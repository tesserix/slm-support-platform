# 07 — Execution Roadmap

The previous docs cover *what* to build and *why*. This doc covers *what to do next*, concretely.

> **Updated after Otto discovery.** A new workstream **D** is now the first thing to land — Otto migration from mark8ly. See [`08-otto-integration.md`](08-otto-integration.md). Workstream A's "pilot strategy" reshapes around mark8ly being the natural first AI-enabled product because that's where Otto already exists.

## The four workstreams

| ID | Workstream | Can start when | Lives in |
|----|------------|---------------|----------|
| **D** | **Otto migration** — move Otto from mark8ly to this repo, stand up shared Otto + support-mongo, repoint mark8ly's proxy | Now (highest priority) | this repo + mark8ly + `tesserix-k8s` |
| **B** | **`tesserix-k8s` scaffold** — namespace, NetworkPolicy, ArgoCD app, ExternalSecrets, KEDA stubs, support-postgres | Now (parallel with D) | `tesserix-k8s/charts/apps/support-platform/` |
| **C** | **Phase 1 from-scratch SLM** — learning track, no production dependency | Now (parallel with D + B) | `phase1-from-scratch/` (this repo) |
| **A** | **AI rollout per product** — slm-inference + slm-router + per-product MCP, then enable AI for mark8ly → fanzone → homechef | D + B + Phase 2A + 2B all done | this repo + product repos + `tesserix-k8s` |

D, B, and C are independent and can all start today. A is gated on D, B, and Phase 2 finishing.

---

## Workstream B — `tesserix-k8s` scaffold

The scaffold can land *before* the model and RAG layer are ready. Placeholder Deployments that wire up wrong but exist let us iterate the manifests and the ArgoCD wiring without waiting on Phase 2.

### B-1. Namespace and base manifests

Create `tesserix-k8s/charts/apps/support-platform/` with the standard Tesserix chart layout. First commit only needs:

- `Chart.yaml`, `values.yaml`, `templates/_helpers.tpl` (boilerplate)
- `templates/namespace.yaml` — `support-platform` namespace with `istio.io/dataplane-mode=ambient` label
- `templates/networkpolicy-hbone.yaml` — explicit allow TCP 15008 to/from the pod CIDR (the silent-failure trap)
- `templates/externalsecret-platform.yaml` — pulls `support-platform-hf-token`, `support-platform-openai-fallback`, MCP API keys from GCP Secret Manager

### B-2. ArgoCD app-of-apps

`tesserix-k8s/argocd/prod/apps/support-platform/` following the existing app-of-apps pattern:

- `app-of-apps.yaml` — points ArgoCD at the chart subdir
- `kustomization.yaml` if needed for app composition
- Auto-sync = false initially; flip to true after first successful sync

### B-3. Support-postgres (CNPG cluster, pgvector)

`templates/support-postgres.yaml` — a CNPG `Cluster` resource in `support-platform` ns:

- Single primary + 1 replica (read-only for retrieval if we ever need read fanout)
- `postgres_extensions: ["vector"]`
- `db-schema-bootstrap` CronJob already in `tesserix-k8s` ingests the schema under `db-schema-bootstrap/schemas/support-platform/support/` (per CLAUDE.md: SQL lives in `tesserix-k8s` only, not in this repo)
- Schema: `chunks(id, namespace, content, embedding vector(384), metadata jsonb)`, indexes on `namespace` and a HNSW index on `embedding`

### B-4. Placeholder Deployments

Stub services with the right names, labels, and ports — image points to a do-nothing `nginx` or a basic Go health server. Lets us:
- Validate Helm renders cleanly
- Confirm ArgoCD sync works
- Test Istio ambient routing
- Test the VirtualService for `chat.tesserix.app`

Stubs to add:
- `slm-inference` Deployment (will become llama.cpp)
- `embedder` Deployment (will become bge-small)
- `reranker` Deployment (will become bge-reranker)
- `support-bff` Knative `Service`
- `support-router` Knative `Service`
- `support-orchestrator` Knative `Service`

### B-5. Edge and routing

`templates/virtualservice-chat.yaml` — route `chat.tesserix.app` through `istio-ingress/tesseract-gateway` to `support-bff.support-platform.svc.cluster.local`. Cloudflare DNS CNAME goes through the existing `external-dns` setup.

### B-6. KEDA ScaledObjects (placeholders)

`templates/scaledobject-slm.yaml` and `templates/scaledobject-reranker.yaml` — same shape as `devai/nemoclaw-inference`. Triggers point at a Prometheus query for queue depth; for now the metric returns 0 and KEDA keeps replicas at `minReplicaCount`.

### B-7. Doc-ingestion CronJob

`templates/cronjob-doc-ingestion.yaml` — mirrors `db-schema-bootstrap` shape, every 30 min. The container image points at a placeholder until C/A produce the real ingestion script. The CronJob exists so the schedule is plumbed and visible in ArgoCD.

### B done condition

- `kubectl get ns support-platform` returns the namespace, ambient-labeled
- `kubectl get all -n support-platform` shows the stub services running (even if doing nothing useful)
- `curl https://chat.tesserix.app/healthz` returns 200 from the stub BFF
- ArgoCD shows the `support-platform` app green
- A KEDA `ScaledObject` exists and is in `Ready=True` even though it isn't scaling anything yet

This is hard infrastructure plumbing. None of it requires the model to exist. Land it early so the model integration is a small change at the end.

---

## Workstream C — Phase 1 from-scratch SLM (parallel learning track)

The Phase 1 plan is already documented in [`03-phase1-build-plan.md`](03-phase1-build-plan.md). Start at the top.

### C-1. Repo setup
- `requirements.txt` — PyTorch 2.x, `datasets`, `tqdm`, `numpy`, `wandb` (optional)
- Python venv inside `phase1-from-scratch/`
- Pick the GPU/CPU/MPS device (Apple Silicon works via MPS)

### C-2. Code files in order (each gets its own commit)

| Step | File | What |
|------|------|------|
| 1 | `01_data.py` | Download TinyStories, build `Dataset` returning `(input_ids, target_ids)` |
| 2 | `02_tokenizer.py` | BPE tokenizer (or use pre-trained, then come back) |
| 3 | `03_embeddings.py` | Token + positional embedding modules |
| 4 | `04_attention.py` | Scaled dot-product + multi-head + causal mask |
| 5 | `05_block.py` | Transformer block: pre-norm + attention + FFN + residuals |
| 6 | `06_model.py` | The full GPT-style model |
| 7 | `07_train.py` | Train loop with warmup + cosine LR |
| 8 | `08_generate.py` | Greedy / temperature / top-k / top-p sampling |

### C done condition

Per `03-phase1-build-plan.md`: model generates coherent toy stories, and you can answer the five "feel obvious" questions in that doc without looking them up.

C feeds nothing into A or B. Its output is *understanding*, not artifacts. The production SLM in Phase 2A is a different model (Qwen2.5-1.5B).

---

## Workstream A — Pilot integration (after Phase 2 + B)

This is the moment of truth: the platform meets a real product and real users. Sequenced for minimum customer risk.

### A-1. Pilot selection (graduated rollout)

| Stage | Audience | Surface | Why this order |
|-------|----------|---------|----------------|
| **Alpha** | Internal Tesserix team only | An internal admin page (e.g., `admin.tesserix.app/support-test`) | Zero customer risk. We see every failure ourselves. |
| **Beta 1** | Tesserix-blog or horoscope visitors | The pilot product's frontend | Low traffic, low support stakes, simple content domain. Recommend **tesserix-blog** because it has real content for the RAG corpus (the blog posts themselves). |
| **Beta 2** | Mark8ly or fanzone visitors | The product's frontend | Real support load, real stakes. After Beta 1 validates the pattern. |
| **GA** | All five products | All product frontends | Onboard `homechef`, `gameverse`, `stockpilot` together once the pattern is stable. |

Each stage is a separate go/no-go decision. The done conditions:
- **Alpha → Beta 1:** zero severe model failures over 200 internal queries; latency p95 < 6s (CPU-only)
- **Beta 1 → Beta 2:** customer satisfaction not noticeably worse than baseline human-served support; <5% of conversations escalated to human
- **Beta 2 → GA:** Beta 2's CSAT holds for 30 days; cost per ticket established and acceptable

### A-2. Per-product onboarding checklist

Same template for each product, once we get to it:

1. **Docs ingested.** Push the product's Markdown docs into the `support-postgres` pgvector namespace `{product}`. The ingestion CronJob picks it up.
2. **MCP server stood up.** Use `scrapper-mcp` as the template:
   - New repo or subdir in the product's repo
   - Python FastMCP, port 8765, `/mcp`, `/healthz`, `/readyz`
   - GH Actions builds to GHCR
   - Subchart `charts/<product>-mcp/` in `tesserix-k8s` with the same shape as `scrapper-mcp`
   - ArgoCD app for the MCP server
   - ExternalSecret for `<product>-mcp-api-key`
3. **Router configuration updated.** Add the product to the support-router's product list and routing rules.
4. **Chat widget mounted.** Pull in the `@tesserix/web` widget component, pass the product identity, deploy the frontend.
5. **Eval set built.** 50–100 (question, ideal-chunk) pairs for this product, hand-built. Held out for evaluation.
6. **Smoke test.** A team member chats end-to-end before customer rollout.

### A-3. Pilot done condition

For the alpha + beta 1 pilot specifically:

- `chat.tesserix.app` widget loaded in `tesserix-blog` frontend
- A customer can ask "what's the latest post about?" and get an actually-correct answer pulled from a real blog post
- A customer can ask "what is Tesserix?" and get a well-shaped reply that does *not* hallucinate
- p95 response latency < 6 seconds on CPU
- Cost per chat under $0.001
- Refusal behaviour works: out-of-scope questions get a polite redirect, not a hallucination

When this works for `tesserix-blog`, Beta 2 starts.

---

## Critical path

```
NOW                                                                                    Beta 1
 |                                                                                       |
 | [B] tesserix-k8s scaffold ─────────────────────────────────┐                          |
 |                                                            ├──→ [A] pilot integration |
 | [Phase 2A] CPU model fine-tune + llama.cpp serving ────────┤                          |
 |                                                            │                          |
 | [Phase 2B] RAG stack, ingestion, reranker ─────────────────┘                          |
 |                                                                                       |
 | [C] Phase 1 learning track (purely educational, no production dependency)             |
 |                                                                                       |
```

B is the rate-limiter you might not expect — it's "easy" but every infrastructure item has someone to coordinate with (DNS, GCP IAM, ArgoCD permissions). Start it early.

## First milestone (smallest end-to-end)

The smallest thing that proves the architecture works is *not* the alpha pilot. It's earlier than that:

**Milestone Zero — "Hello world from the SLM platform"**

- `support-platform` ns exists, ambient-labeled
- `slm-inference` pod runs llama.cpp serving a stock Qwen2.5-1.5B (no fine-tune, no RAG)
- `support-bff` proxies HTTP to llama.cpp's OpenAI-compatible endpoint
- `curl chat.tesserix.app/v1/completions -d '{"prompt":"hello"}'` returns model output

That's it. No router, no RAG, no MCP, no widget. Stock model parrot. But it proves: GKE deployment, GPU/CPU node behaviour (CPU here), Istio routing, ArgoCD wiring, KEDA scale-from-1, secrets plumbing, monitoring.

If we can hit Milestone Zero, everything else is incremental. If we can't, every later milestone is blocked.

**Aim to land Milestone Zero as the first meaningful Phase 3 deliverable.** It happens before fine-tuning matters, before RAG matters, before MCP matters. After Milestone Zero, each Phase 2 deliverable can be deployed independently and tested in isolation.

## Open decisions still needed

These will eventually need answers, but don't block starting B or C:

- **Which model fine-tune base?** Qwen2.5-1.5B is the current pick — but bake off against SmolLM2-1.7B and TinyLlama-1.1B on a Tesserix-style eval set before committing.
- **Vector DB: pgvector or Qdrant?** Recommending pgvector in CNPG (already operational) for v1. Qdrant if recall plateaus at scale.
- **Chat widget design.** Whose visual design system — the existing `@tesserix/web` button language, or a chat-specific look? Probably reuse `@tesserix/web` shadcn/Radix primitives.
- **Audit logging.** Where do chat transcripts get stored for QA / training data flywheel? Likely `support-postgres` with PII-scrub.
- **Rate limiting.** Per-user limits to prevent abuse. Use the existing OpenFGA + auth-bff pattern for this.

These get resolved during B's plumbing work and in the first chat-widget design pass.
