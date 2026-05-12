# slm-router

The gateway between Otto (chat) and the SLM stack (RAG, embedder, reranker, slm-inference, per-product MCP servers).

```
Otto MongoDB
   │ change stream
   ▼
slm-router  ──→  embedder ──→ pgvector ──→ reranker
   │                  ↓
   │             top-K chunks
   │                  ↓
   └──→ slm-inference (OpenAI-compatible HTTP)
            │
            └──→ tool call ──→ <tenant>-mcp ──→ product API
                                      │
                                      ▼
                          slm-router posts reply to Otto as
                          SenderAssistant (role="assistant")
```

Configuration is split:

- **Operational** — env vars (`HTTP_PORT`, `MONGO_URI`, `INFERENCE_URL`, ...). Rotated by ExternalSecrets and platform owners. See `internal/config/config.go::Env`.
- **Tenant routing** — `routes.yaml` mounted from a ConfigMap. Updated by product teams and the ML team. See `internal/config/config.go::Routes`.

## Endpoints

| Path | Purpose |
|------|---------|
| `GET /healthz` | Liveness |
| `GET /readyz`  | Readiness; 503 until dependencies are confirmed up |
| `GET /metrics` | (planned) Prometheus metrics |
| `POST /v1/replay` | (planned, dev-only) Replay a conversation through the orchestrator |

The hot path is **event-driven**, not HTTP. slm-router watches Otto's `conversations.messages` collection change stream and pushes new customer messages into the orchestrator goroutine.

## Run locally

```
cp .env.example .env
go run ./cmd/server
```

The skeleton starts and serves `/healthz` + `/readyz`. The Mongo watcher and orchestrator are added in subsequent commits (tasks D5 + D6).

## Tests

```
go test ./...
```

## Layout

```
services/slm-router/
├── cmd/server/main.go
├── internal/
│   ├── config/      env + YAML routes loader
│   ├── httpserver/  Gin engine, health/ready
│   ├── logger/      slog wrapper
│   ├── watcher/     (D5) Mongo change-stream consumer
│   ├── orchestrator/(D6) RAG → SLM → MCP → reply
│   ├── inference/   (D6) slm-inference HTTP client
│   ├── embed/       (D6) embedder HTTP client
│   ├── rerank/      (D6) reranker HTTP client
│   ├── mcp/         (D6) MCP JSON-RPC client + registry
│   └── escalation/  (D6) confidence + keyword evaluator
├── Dockerfile
└── README.md
```
