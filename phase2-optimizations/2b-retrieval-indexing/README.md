# Phase 2B — Retrieval & Indexing Optimizations (code)

Code for the retrieval, indexing, chunking, and reranking optimizations. Plan and step-by-step explanations: [`../../docs/05-phase2b-retrieval-indexing.md`](../../docs/05-phase2b-retrieval-indexing.md).

## Planned files

```
2b-retrieval-indexing/
├── 01_corpus_assemble.py        per-product corpus pull + cleanup
├── 02_chunking.py               chunking strategies bake-off
├── 03_embeddings.py             pick + benchmark an embedding model
├── 04_index_types.py            flat vs HNSW vs IVF — speed/memory/recall
├── 05_hybrid_search.py          dense + BM25 hybrid retrieval
├── 06_rerank.py                 cross-encoder reranker on top-K results
├── 07_query_rewriting.py        HyDE / multi-query / decomposition
├── 08_agentic_rag.py            self-RAG / corrective-RAG / agentic-RAG
├── 09_ingestion_pipeline.py     end-to-end doc ingestion (Markdown → namespace)
└── 10_retrieval_eval.py         recall@K, MRR, hit-rate, per-product eval sets
```

Nothing is here yet. Read the plan doc first.
