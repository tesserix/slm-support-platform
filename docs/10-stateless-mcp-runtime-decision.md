# Stateless product MCP runtime decision

## Decision

All active product MCP servers use the published Tesserix MCP Runtime and its
stateless Streamable HTTP transport. The router and servers use protocol
`2026-07-28`; every `server/discover`, `tools/list`, and `tools/call` request is
independently complete. `Mcp-Session-Id` is rejected and no workload requires
sticky routing, shared request memory, or pod affinity.

The Customer AI Registry remains the control-plane registry. It owns the YAML
manifest, Tesserix annotations, qualification evidence, immutable versions,
and gateway export status. The Solo.io registry is not used.

## Runtime integration

The shared `mcp-gateway` image pins `tesserix-mcp-runtime v0.1.0-rc.6` by its
GitHub Release wheel URL and SHA-256 digest. Product code adapts the existing
tenant tool registry through the runtime's protocol endpoint interface. The
runtime owns HTTP and JSON-RPC parsing, protocol discovery, stateless session
enforcement, bounded headers/request/response bodies, cancellation, draining,
DNS rebinding protection, origin checks, health endpoints, and metrics.

Authentication is fail-closed. `X-MCP-Key` is verified in constant time by the
runtime context provider before the body is parsed. A `tools/call` is admitted
only when its trusted `X-Tenant-Id` equals the pod's configured tenant. Customer
and conversation headers enter a request-local context only after those checks.
The local no-auth escape hatch remains explicit and is not enabled in production.

## Failure and SLO behavior

- Authentication or tenant mismatch returns a bounded `401` without parsing or
  reflecting the request body.
- Unsupported protocol metadata and session-bearing requests fail closed.
- Requests above 64 KiB, responses above 512 KiB, more than 128 tools, or a
  stream over 30 seconds are rejected by runtime limits.
- `/startupz`, `/livez`, `/readyz`, and `/metrics` remain credential-free for
  Kubernetes and monitoring. Readiness closes during drain.
- Product backend calls retain their four-second budget and structured
  unavailable/not-implemented results, allowing the router to escalate without
  fabricating customer data.

The availability target remains the support platform SLO. Stateless operation
allows ordinary Kubernetes round-robin traffic and replica replacement without
conversation loss; backend latency and failures remain visible per tool.

## Compatibility and rollout

The Go router sends the protocol version, method, optional tool name, client
capabilities, and client identity on every request. It discovers support before
listing tools. The same shared image serves HomeChef, Mark8ly, platform, and
Stockpilot, so one immutable promotion updates their server behavior consistently.

Fanzone remains decommissioned. Gameverse and Horoscope are manifest-aligned but
not activated. Their presence in configuration is not authorization to deploy.

Rollout is GitOps-only. CI publishes an immutable image, Kargo promotes its
digest, and Argo CD reconciles the owning Applications. Rollback means restoring
the previous known-good image digest through the same GitOps path; credentials
are not rotated for a protocol rollback.

## Qualification evidence

Release qualification must prove unauthenticated rejection, successful
authenticated discovery, independent tool listing, session rejection, bounded
requests, tenant isolation, two-instance stateless equivalence, health/metrics,
and router list/call compatibility. Customer AI Registry export remains blocked
until those probes pass against the deployed digest.
