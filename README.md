# slm-support-platform

Building a Small Language Model from scratch, then growing it into an agentic support chatbot that handles customer queries across the Tesserix product portfolio (mark8ly, fanzone, homechef, gameverse, stockpilot, …).

This repo is **learning-first**. Phase 1 is pedagogy — understand transformers by building one. Phases 2 and 3 take an open-weight small model into production behind the products on GKE.

---

## The 3-phase roadmap

| Phase | Goal | Model | Outcome |
|-------|------|-------|---------|
| **1. From-scratch SLM** | Understand transformers end-to-end | ~10–50M params, decoder-only, trained on TinyStories | Working tiny GPT, generates coherent toy text, you understand every line |
| **2. Fine-tune an open SLM** | Adapt a real SLM to Tesserix domain | Phi-3-mini / Llama-3.2-1B / Gemma-2B with LoRA on product docs | Model that speaks "Tesserix" — knows our products, terminology, support patterns |
| **3. Agentic support platform** | Production chatbot per product | Phase-2 model + per-product RAG namespaces + router agent + tools | Live chatbot routed per product, deployed on GKE with Istio + ArgoCD |

Phase 1 is where we're starting. Don't skip ahead.

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
├── README.md                          this file
├── docs/
│   ├── 01-slm-fundamentals.md         what is an SLM, vs LLM, why small
│   ├── 02-end-state-architecture.md   the production support chatbot vision
│   ├── 03-phase1-build-plan.md        what we'll build in Phase 1, step by step
│   └── diagrams/
│       ├── end-state.md               Mermaid: full prod system
│       └── transformer-block.md       Mermaid: attention + FFN inside one block
├── phase1-from-scratch/               PyTorch code lands here as we build
├── phase2-fine-tuning/                placeholder — LoRA / QLoRA scripts later
├── phase3-agentic-support/            placeholder — RAG + agent router + tools
└── data/                              datasets (gitignored)
```

---

## Where to start reading

1. [`docs/01-slm-fundamentals.md`](docs/01-slm-fundamentals.md) — what SLMs are and why we're using one
2. [`docs/02-end-state-architecture.md`](docs/02-end-state-architecture.md) — the destination, so the learning has purpose
3. [`docs/03-phase1-build-plan.md`](docs/03-phase1-build-plan.md) — the actual Phase 1 build sequence

Then we start writing code in [`phase1-from-scratch/`](phase1-from-scratch/).
