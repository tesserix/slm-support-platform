# slm-support-platform

Building a Small Language Model from scratch, then growing it into an agentic support chatbot that handles customer queries across the Tesserix product portfolio (mark8ly, fanzone, homechef, gameverse, stockpilot, …).

This repo is **learning-first**. Phase 1 is pedagogy — understand transformers by building one. Phases 2 and 3 take an open-weight small model into production behind the products on GKE.

---

## The 3-phase roadmap

| Phase | Goal | Outcome |
|-------|------|---------|
| **1. From-scratch SLM** | Understand transformers end-to-end | Working tiny GPT (~25M params) on TinyStories, you understand every line |
| **2. Optimizations** (two parallel sub-tracks) | Make the model + retrieval production-grade | Fine-tuned SLM at <500ms p95, tuned per-product RAG with reranker and ingestion pipeline |
| **2A. Model & Serving** | LoRA fine-tune + inference optimizations | Phi-3-mini / Llama-3.2-3B with KV cache, quantization, batching, FlashAttention, speculative decoding, vLLM tuning |
| **2B. Retrieval & Indexing** | Production-grade RAG stack | Per-product index, smart chunking, hybrid search, cross-encoder reranking, query rewriting, agentic RAG patterns, doc-ingestion pipeline |
| **3. Agentic support platform** | Production chatbot integrated across products | Phase-2 components wired up: support-bff, router agent, MCP, tool layer, deployed on GKE with Istio + ArgoCD |

Phase 1 is where we're starting. Phase 3 doesn't start until both 2A and 2B finish (the chosen "optimize fully before integration" path).

---

## Why this order, not "just use GPT-4"

| | Off-the-shelf LLM (GPT-4 / Claude) | Our SLM path |
|---|---|---|
| Per-query cost | ~$0.01–0.10 | ~$0.0001 (self-hosted GPU) |
| Latency | 1–3s round-trip | <500ms in-cluster |
| Data leaves your cluster | Yes | No |
| Domain adaptation | Prompt engineering only | Fine-tuning on your data |
| You learn how it works | No | Yes |

The from-scratch step in Phase 1 doesn't produce the production model — it produces *you*, equipped to make real decisions in Phases 2 and 3.

---

## Repo layout

```
slm-support-platform/
├── README.md                              this file
├── docs/
│   ├── 01-slm-fundamentals.md             what is an SLM, vs LLM, why small
│   ├── 02-end-state-architecture.md       the production support chatbot vision
│   ├── 03-phase1-build-plan.md            from-scratch transformer build
│   ├── 04-phase2a-model-serving.md        LoRA + KV cache + quant + batching + vLLM
│   ├── 05-phase2b-retrieval-indexing.md   chunking + index + hybrid + rerank + agentic RAG
│   └── diagrams/
│       ├── architecture.drawio            full system in drawio / Lucidchart
│       ├── customer-flow.drawio           swim-lane request flow
│       └── *.md                           Mermaid sources
├── phase1-from-scratch/                   from-scratch transformer code
├── phase2-optimizations/
│   ├── 2a-model-serving/                  fine-tune + inference optimizations
│   └── 2b-retrieval-indexing/             RAG stack
├── phase3-agentic-support/                BFF + router + MCP + tools (after Phase 2)
└── data/                                  datasets (gitignored)
```

---

## Where to start reading

1. [`docs/01-slm-fundamentals.md`](docs/01-slm-fundamentals.md) — what SLMs are and why we're using one
2. [`docs/02-end-state-architecture.md`](docs/02-end-state-architecture.md) — the destination, so the learning has purpose
3. [`docs/03-phase1-build-plan.md`](docs/03-phase1-build-plan.md) — the actual Phase 1 build sequence
4. [`docs/04-phase2a-model-serving.md`](docs/04-phase2a-model-serving.md) — Phase 2A plan: fine-tuning + inference optimizations
5. [`docs/05-phase2b-retrieval-indexing.md`](docs/05-phase2b-retrieval-indexing.md) — Phase 2B plan: retrieval stack
6. [`docs/06-gke-deployment-plan.md`](docs/06-gke-deployment-plan.md) — actual deployment plan for `tesseract-prod-in-gke` (CPU-only path)
7. [`docs/07-execution-roadmap.md`](docs/07-execution-roadmap.md) — what to do next: three parallel workstreams, pilot strategy, Milestone Zero

### Architecture diagrams (drawio / Lucidchart)

- [`docs/diagrams/architecture.drawio`](docs/diagrams/architecture.drawio) — full system: products → chat widget → edge → support platform (BFF, Router, Orchestrator, MCP, RAG, SLM) → per-product MCP servers → product APIs
- [`docs/diagrams/customer-flow.drawio`](docs/diagrams/customer-flow.drawio) — one customer chat message travelling through every component, end to end, in swim lanes
- [`docs/diagrams/README.md`](docs/diagrams/README.md) — how to open them (diagrams.net, Lucidchart, VS Code Draw.io extension)

Then we start writing code in [`phase1-from-scratch/`](phase1-from-scratch/).
