# Phase 2 — Optimizations

Phase 2 is where the system stops being a toy. Phase 1 gave you a working from-scratch transformer to understand internals. Phase 2 fine-tunes a real open SLM and makes the whole inference + retrieval pipeline fast, accurate, and cheap enough to actually run in production.

Phase 3 (integration: BFF, router, MCP, tools, GKE deployment) does **not** start until Phase 2 is done — you only deploy a model and a retriever you know are tuned.

## Two parallel sub-tracks

These two tracks can be worked in any order or in parallel. They optimise different parts of the stack and don't depend on each other.

### 2A — Model & Serving Optimizations

Make the **model** good (LoRA fine-tune on Tesserix data) and make **inference** fast and cheap (KV cache, quantization, batching, FlashAttention, vLLM tuning).

- Plan: [`../docs/04-phase2a-model-serving.md`](../docs/04-phase2a-model-serving.md)
- Code lands in: [`2a-model-serving/`](2a-model-serving/)

### 2B — Retrieval & Indexing Optimizations

Make the **knowledge layer** good (clean corpus, smart chunking, the right index, hybrid search, reranking, query rewriting, agentic RAG patterns).

- Plan: [`../docs/05-phase2b-retrieval-indexing.md`](../docs/05-phase2b-retrieval-indexing.md)
- Code lands in: [`2b-retrieval-indexing/`](2b-retrieval-indexing/)

## Why optimize before integration (the chosen order)

The roadmap chose "optimize fully before Phase 3 integration" rather than "ship naive then optimize." That means:

- **The platform launches already tuned.** No "we'll improve it later" debt at launch.
- **Phase 3 becomes pure plumbing.** When you build the BFF, router, and MCP layer, you're integrating known-good components, not changing them.
- **You learn each optimization in isolation**, which is the whole point of this repo — production speed-ups are easy to copy from a blog post, but understanding *why* a particular optimization helps requires having built the un-optimized version first.

The cost: longer time-to-first-working-chatbot. Phase 3 doesn't start until both 2A and 2B finish.

## Done condition

Phase 2 is "done" when:

- **2A:** a fine-tuned SLM is serving at <500ms p95 latency on a single L4 GPU, quantized, with vLLM continuous batching enabled, and beats the base model on a Tesserix-specific eval set.
- **2B:** a per-product RAG index returns relevant chunks at recall@10 above the baseline on a hand-built eval set for each product, with a reranker stage and a doc-ingestion pipeline that keeps namespaces in sync as product docs change.

When both are true, Phase 3 starts.
