# Phase 3 — Agentic Support Platform

Placeholder. Built on top of the Phase 2 fine-tuned model.

## Plan (subject to refinement once Phase 2 ships)

- `support-bff` (Go / Gin) — same patterns as other Tesserix BFFs
- Router agent — per-product context detection
- Vector DB (Qdrant or pgvector) with per-product namespaces
- Doc-ingestion CronJob in `tesserix-k8s` (mirrors `db-schema-bootstrap` pattern)
- vLLM-based serving for the fine-tuned SLM
- Tool layer that calls existing product APIs (homechef-api, mark8ly APIs, etc.)
- Helm chart in `tesserix-k8s/charts/apps/support-platform/`
- ArgoCD app under `tesserix-k8s/argocd/prod/apps/support-platform/`
- Chat widget package consumable by each product's frontend
