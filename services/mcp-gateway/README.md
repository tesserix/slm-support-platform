# mcp-gateway

A single stateless MCP server image that serves the Otto MCP endpoint for any
Tesserix product. Tools are tenant-scoped: the `MCP_TENANT` env var
selects which tool group is exposed.

Replaces the per-product nginx stub in `tesserix-k8s/charts/apps/mcp-stub/`.
Same per-product hostname (e.g. `fanzone-mcp.fanzone.svc.cluster.local:8765`),
using MCP `2026-07-28` discovery and real tool definitions.

## What the AI gets

For each tenant the gateway exposes a small, tenant-scoped support surface:

| Tenant | Tools (read-only) |
| --- | --- |
| mark8ly | `get_order`, `list_returns`, `check_payment_status` |
| fanzone | `get_user_points`, `get_match_info`, `list_user_predictions` |
| homechef | `get_order_status`, `get_chef_availability`, `track_delivery` |
| stockpilot | `get_portfolio_summary`, `get_broker_status`, `get_agent_trace` |
| gameverse | `get_room_state`, `get_user_rating`, `get_match_history` |
| horoscope | `get_chart_summary`, `get_today_transit`, `list_recent_readings` |
| scrapper | `get_scrape_job`, `list_publishing_pipelines`, `list_connected_accounts` |

Plus two cross-tenant tools that always work:

- `search_knowledge_base(query, limit)` — semantic search of the
  tenant's pgvector chunks (delegates to slm-router's retriever).
- `lookup_conversation(conversation_id)` — pull the recent message
  timeline for follow-up context.

Every network deployment requires `MCP_AUTH_KEY`; unauthenticated mode needs
the explicit local-only `MCP_ALLOW_INSECURE_NO_AUTH=true` override. Each
`tools/call` also requires the trusted tenant header to match the pod's
`MCP_TENANT`. Write tools derive customer and conversation identity from those
trusted request headers, never from model-supplied arguments, and delegate
deduplication to the owning product backend.

## Tool implementation status

The first iteration of every product-specific tool returns a
**realistic stub response** with the same JSON shape a real backend
would return, plus an explicit `"_stub": true` flag so the AI knows
to caveat its answer. Product teams replace the stub bodies with real
HTTP calls when the upstream APIs are stable.

This is intentional: a stub-shaped response is much more useful to the
LLM than no tool at all — the model learns the tool exists, the shape
of its response, and how to weave the answer into a reply.

## Run

```bash
# Locally, against the fanzone tool set:
docker build -t mcp-gateway services/mcp-gateway
docker run --rm -e MCP_TENANT=fanzone -e MCP_AUTH_KEY=dev-only -e MCP_PORT=8765 -p 8765:8765 mcp-gateway
curl http://localhost:8765/mcp -X POST \
  -H 'Content-Type: application/json' \
  -H 'X-MCP-Key: dev-only' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'MCP-Method: server/discover' \
  -d '{"jsonrpc":"2.0","id":1,"method":"server/discover","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":{"name":"curl","version":"1"}}}}'
```

## Replace the stub

Once this image is built and pushed:

1. `charts/apps/mcp-gateway/` Helm chart replaces `mcp-stub/`.
2. Per-product ArgoCD apps in `argocd/prod/apps/ai-apps/<tenant>-mcp.yaml`
   point at the new chart with `mcpTenant: <tenant>`.
3. slm-router's tool discovery now returns the real tool list and the
   agent loop can call them as part of the answer.
