# 05 — Phase 2B: Retrieval & Indexing Optimizations

Goal of Phase 2B: build a retrieval layer for the support chatbot that returns *the right chunks* fast — per product, at high recall, with reranking, with query rewriting, and with the patterns that let an agent decide *when* to retrieve at all.

In Phase 3 the model uses this retrieval layer through the orchestrator and MCP. In Phase 2B we build and tune the retrieval layer in isolation, with an eval set, so we know it's good before any agent loop touches it.

This phase is independent of Phase 2A — 2A makes the model fast, 2B makes the knowledge layer accurate. Both can run in parallel.

## What "good retrieval" actually means

Retrieval is the most under-appreciated lever in a chatbot. A fine-tuned SLM with bad retrieval hallucinates confidently; a base SLM with great retrieval is often indistinguishable from a much bigger model. The metrics that matter:

- **Recall@K** — of the top K results, how often does the actually-correct chunk appear? Target: >90% at K=10 for in-corpus questions.
- **MRR (Mean Reciprocal Rank)** — how high up the list does the right chunk appear? Closer to 1 = right answer is rank 1 more often.
- **Hit rate** — for ambiguous queries, does *any* useful chunk appear?

Build a per-product eval set early. Without one, every "improvement" is vibes.

## The retrieval stack (bottom-up view)

```
                Customer query
                     |
   query rewriting   |  rewrite / HyDE / decompose
                     v
   hybrid retrieval  |  dense vector + BM25, combined
                     v
   top-N candidates  |  N = 50, say
                     v
   reranker          |  cross-encoder re-scores
                     v
   top-K survivors   |  K = 5 chunks to feed the model
                     v
                   Model
```

Each step in this doc maps to one of those layers.

## Step-by-step plan

### Step 1 — Corpus assembly

Pull every textual artifact each Tesserix product has:

- **Product docs** — Markdown from the docs site
- **FAQ pages** — user-facing Q/A
- **Support transcripts** — historical chats, anonymised
- **API docs** — for technical questions
- **Known-issue write-ups** — incident postmortems re-shaped for customer language

One per-product corpus, kept separate. The doc-ingestion pipeline (Step 9) keeps these synced with the source repos.

**Files:** `2b-retrieval-indexing/01_corpus_assemble.py`

### Step 2 — Chunking strategies

How you split text into chunks decides what gets retrieved. The choices:

- **Fixed-size** — split every N tokens, with overlap. Simplest. Bad at boundaries.
- **Sentence-based** — split at sentence boundaries, group sentences until token budget. Better, still ignores semantics.
- **Semantic / markdown-aware** — split at Markdown headers, list boundaries, paragraph breaks. Best for structured docs. What we'll default to.
- **Hierarchical (parent-child)** — store small chunks for retrieval, but return their larger parent chunk to the model. Best for long docs where a small chunk lacks enough context to answer.

Chunk size matters too:

- Too small (50 tokens) → fragmented, model lacks context
- Too large (2k tokens) → expensive to embed, retrieval gets less specific, prompt blows up
- Sweet spot for support: ~250–500 tokens with 50-token overlap

**What you'll learn:** chunking is half the retrieval problem. A perfect index over bad chunks underperforms a naive index over good chunks.

**Files:** `2b-retrieval-indexing/02_chunking.py` — bake off the four strategies on a sample corpus, measure recall@10.

### Step 3 — Embedding model

What turns a chunk into a vector. Options:

| Model | Dim | Pros | Cons |
|-------|-----|------|------|
| **OpenAI text-embedding-3-small** | 1536 | Strong baseline, cheap API | External call, data leaves cluster |
| **bge-large-en-v1.5** | 1024 | Open-weight, strong, self-host | Larger memory footprint |
| **e5-large-v2** | 1024 | Open, strong on retrieval bench | Older |
| **bge-small-en-v1.5** | 384 | Very fast, smaller index | Quality drop on hard queries |
| **Tesserix-fine-tuned embed** | 384–1024 | Could learn product terminology | Real effort to build, only worth it later |

Default: **bge-large-en-v1.5** self-hosted. Tesserix-specific fine-tuning can come later if recall plateaus.

**Files:** `2b-retrieval-indexing/03_embeddings.py`

### Step 4 — Index types

The data structure that lets you find nearest neighbours of a query embedding fast.

- **Flat / brute-force** — compare query against every chunk. Perfect recall. O(N) per query. Fine up to ~100k chunks; falls over after that.
- **HNSW (Hierarchical Navigable Small World)** — graph-based. Sub-linear query time. Slight recall loss (~98% of flat). The most common choice. Default in Qdrant, pgvector (with index), Pinecone.
- **IVF (Inverted File Index)** — coarse cluster, then exact within cluster. Faster build time than HNSW but lower recall.
- **PQ (Product Quantization)** combined with IVF/HNSW — compress vectors to use less RAM. Trade recall for memory.

For our scale (a few products × few thousand chunks each = ~50k chunks), flat works. As we grow toward 1M+, switch to HNSW. Start flat to keep ground truth recall, then move and measure recall delta.

**What you'll learn:** ANN (approximate nearest neighbour) is a recall-speed-memory triangle. You always trade two for the third.

**Files:** `2b-retrieval-indexing/04_index_types.py` — benchmark flat vs HNSW vs IVF on the same corpus.

### Step 5 — Hybrid search (dense + sparse)

Pure dense retrieval misses exact-keyword queries. "Order #12345" doesn't embed well — embeddings put it near other "order #..." numbers, not specifically your number. BM25 (sparse, lexical) shines at exact matches.

Hybrid retrieval:
1. Dense search returns top N (say 50) by embedding similarity
2. BM25 returns top N by keyword match
3. Combine via Reciprocal Rank Fusion (RRF) or weighted score

For support chat: hybrid almost always beats pure dense. Customer queries mix natural-language ("my food is late") with exact tokens ("transaction ID 4f7a...", "error code 502").

Implementations: **Qdrant** has hybrid built in (BM25 + dense). **pgvector** + a separate `ts_vector` BM25 index works in Postgres. **Elasticsearch** + dense plugin works if you already have Elastic.

**Files:** `2b-retrieval-indexing/05_hybrid_search.py`

### Step 6 — Reranking

Retrieval returns top-50 candidates. A reranker re-scores them and returns the top-5 to send to the model. Reranking uses a cross-encoder (sees query and chunk together) instead of a bi-encoder (sees them separately) — much higher quality, much slower, so only practical on a small candidate set.

Cross-encoder options:
- **bge-reranker-large** — open, strong
- **Cohere Rerank API** — hosted, fast, paid
- **ms-marco-MiniLM-L-12-v2** — older but small/fast

Default: **bge-reranker-large** self-hosted.

Pipeline: retrieve 50 → rerank → keep top 5 → pass to model. Reranker latency on 50 chunks: ~50–100ms on CPU, ~10ms on GPU. Worth it for the quality gain.

**What you'll learn:** the difference between a bi-encoder (embeds query and chunk separately, fast, lower quality) and a cross-encoder (jointly encodes query and chunk, slow, higher quality). Why a two-stage retrieve-then-rerank is the standard architecture.

**Files:** `2b-retrieval-indexing/06_rerank.py`

### Step 7 — Query rewriting

Raw customer queries are often bad retrieval inputs:

- "It's broken" — what's broken?
- "Where's my food" — embed of this is generic; actual order info is in the user context
- "Why did you charge me twice" — emotional, low keyword signal

Techniques:

- **Query expansion** — generate paraphrases of the query, retrieve for each, merge. Catches more relevant chunks.
- **HyDE (Hypothetical Document Embeddings)** — instead of embedding the query, ask the SLM to generate a *hypothetical answer*, then embed that. Often works better because embedding space matches answer-shape better than question-shape.
- **Query decomposition** — break "where's my food and can I get a refund" into two queries, retrieve for each.
- **Contextualization** — prepend product context and known user metadata to the query before embedding.

Each technique is a separate experiment with its own eval. Don't enable them all blindly — measure which actually help, because some add latency without recall gain.

**Files:** `2b-retrieval-indexing/07_query_rewriting.py`

### Step 8 — Agentic RAG patterns

Naive RAG: retrieve once, generate once. Often wrong because the model can't say "actually, I need different context."

More sophisticated patterns:

- **Self-RAG** — model emits special tokens to indicate when retrieval is needed and to critique retrieved chunks. Lets the model skip retrieval for chitchat ("hi") and double-check retrieved info before using it.
- **Corrective RAG (CRAG)** — after retrieval, an evaluator scores chunk relevance. If low, re-retrieve with a refined query or fall back to web search.
- **Agentic RAG** — the model is given retrieval as a *tool* (alongside the MCP tools). It decides when to retrieve, what to retrieve, and stops when it has enough context. Best fit for our Phase 3 agent layer.

For Phase 2B, prototype the patterns in isolation (without the full agent). The chosen pattern integrates in Phase 3.

**What you'll learn:** retrieval and generation aren't separate steps — the model can drive retrieval, retrieval can drive generation, and the best system loops between them.

**Files:** `2b-retrieval-indexing/08_agentic_rag.py`

### Step 9 — Doc ingestion pipeline

Production needs the corpus to stay in sync with the source product docs. The pipeline:

1. **Source** — each product's docs repo (mark8ly/docs, homechef/docs, …)
2. **Watcher** — on commit, trigger ingestion (or batch via a CronJob every 30 min, same pattern as `db-schema-bootstrap` in `tesserix-k8s`)
3. **Chunker** — apply chunking strategy
4. **Embedder** — generate embeddings for each chunk (skip unchanged chunks via hash)
5. **Indexer** — upsert into the right Vector DB namespace
6. **Reranker prep** — no offline step needed; reranker runs at query time
7. **Monitoring** — emit a metric on ingestion latency and chunk count per namespace

Lives as a CronJob in `tesserix-k8s/charts/apps/support-platform/` once Phase 3 starts.

**Files:** `2b-retrieval-indexing/09_ingestion_pipeline.py` (local dev version; Helm chart in tesserix-k8s)

### Step 10 — Per-product evaluation

Without an eval set, all of the above is vibes. For each product:

- **Hand-build 50–100 (question, ideal-chunk) pairs.** Spread across question types: factual, navigation, troubleshooting, ambiguous.
- **Hold out 20% as test set; don't use during tuning.**
- **Metrics:** recall@1, recall@5, recall@10, MRR, hit rate

Run the eval after each meaningful change to the retrieval stack. If a change doesn't move the metrics, revert it — added complexity isn't free.

**What you'll learn:** evaluation is the gating function on every optimization. Without it, you're just adding features.

**Files:** `2b-retrieval-indexing/10_retrieval_eval.py`

## Target outcomes for Phase 2B

By the end you should be able to answer, without looking it up:

- Why does chunking matter as much as the embedding model?
- When does pure dense retrieval beat BM25, and vice versa?
- Why is a two-stage retrieve-then-rerank standard?
- When does HyDE help and when does it hurt?
- Why per-product namespaces beat one big shared index?
- How would you debug "the chatbot keeps citing the wrong product's docs"?

When those feel obvious, Phase 2B is done.

## What we're explicitly NOT doing in Phase 2B

- **Multi-modal RAG (images, video).** Text-only first.
- **Graph RAG (knowledge graph backing the retriever).** Useful but a much bigger lift; only worth it if standard RAG plateaus.
- **Fine-tuning the embedding model on Tesserix data.** Worth doing eventually if recall plateaus; not the first lever to pull.
- **Hosted vector DBs (Pinecone, Weaviate Cloud).** We're self-hosting Qdrant or pgvector in-cluster — same reasons we self-host the SLM (cost, data residency, latency).

## Dependency on Phase 2A

None. 2A optimises model + inference. 2B optimises retrieval + indexing. Run in parallel.

## What hands off to Phase 3

After Phase 2B:

- A tuned per-product RAG index, served from an in-cluster Qdrant or pgvector
- A reranker service
- A doc-ingestion CronJob spec ready to drop into `tesserix-k8s`
- An eval set per product, runnable in CI to catch retrieval regressions

Phase 3 picks these up and integrates them with the orchestrator, MCP layer, and BFF.
