"""mcp-gateway HTTP server.

Two transport layers in one app:

1. Plain JSON-RPC 2.0 over HTTP POST at ``/mcp``. This is the
   transport the slm-router's MCP client currently speaks (single
   POST per call, ``Accept: application/json``). We implement it
   directly instead of going through FastMCP's streamable HTTP
   transport so the router doesn't need to know about session ids,
   SSE, or 307 redirects.

2. (Future) FastMCP's streamable HTTP app mounted at ``/streamable``
   for when the router moves to a more capable MCP client.

Tools are defined once in ``tenants.py`` / ``shared_tools.py`` and
registered into a tiny ``ToolRegistry``; both transports share the
same registry so behaviour stays consistent.
"""
from __future__ import annotations

import contextvars
import inspect
import json
import logging
import re
from typing import Any, Awaitable, Callable

from starlette.applications import Starlette
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from .config import Config, load
from . import auto_tools, shared_tools, tenants

logger = logging.getLogger(__name__)

# Trusted conversation/customer context, forwarded by slm-router as HTTP
# headers on every MCP request (set ONLY after the X-MCP-Key shared secret
# is validated by BearerAuthMiddleware — so these are trustworthy). Tools
# read them via tenants._trusted_ctx() to attribute actions (ticket /
# refund-request creation) to the originating conversation. The header
# names MUST match slm-router's mcp.Header* consts.
_TRUSTED_HEADERS: dict[str, str] = {
    "X-Otto-Conversation-Id": "conversation_id",
    "X-Tenant-Id": "tenant_id",
    "X-Store-Id": "store_id",
    "X-Customer-Id": "customer_id",
    "X-Customer-Email": "customer_email",
    "X-Customer-Name": "customer_name",
    "X-Otto-Case-Id": "case_id",
}

# Per-request trusted context. Default empty; set per JSON-RPC request.
request_ctx: contextvars.ContextVar[dict[str, str]] = contextvars.ContextVar(
    "mcp_request_ctx", default={}
)


# ---------------------------------------------------------------------------
# Tool registry — what every transport shares.
# ---------------------------------------------------------------------------
class ToolRegistry:
    """A trivial replacement for FastMCP's tool registration that emits
    JSON-RPC-shaped responses on demand. Tools are async callables.
    """

    def __init__(self, *, allow_mutations: bool = False) -> None:
        self._tools: dict[str, dict[str, Any]] = {}
        self._allow_mutations = allow_mutations

    def tool(self, *, name: str, description: str = "", mutating: bool = False) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        def decorator(fn: Callable[..., Any]) -> Callable[..., Any]:
            if mutating and not self._allow_mutations:
                return fn
            self._tools[name] = {
                "fn": fn,
                "description": description,
                "input_schema": _build_input_schema(fn),
            }
            return fn

        return decorator

    def list_tools(self) -> list[dict[str, Any]]:
        return [
            {
                "name": name,
                "description": entry["description"],
                "inputSchema": entry["input_schema"],
            }
            for name, entry in self._tools.items()
        ]

    async def call(self, name: str, arguments: dict[str, Any]) -> Any:
        if name not in self._tools:
            raise KeyError(name)
        fn = self._tools[name]["fn"]
        result = fn(**(arguments or {}))
        if inspect.isawaitable(result):
            result = await result
        return result


def _build_input_schema(fn: Callable[..., Any]) -> dict[str, Any]:
    """Build a JSON-Schema for a tool from its Python signature.

    Keeps it minimal — every primitive Python type maps to a JSON
    type, defaults become non-required, no fancy validation.
    """
    sig = inspect.signature(fn)
    properties: dict[str, Any] = {}
    required: list[str] = []
    for pname, param in sig.parameters.items():
        prop: dict[str, Any] = {"type": _py_type(param.annotation)}
        if param.default is inspect.Parameter.empty:
            required.append(pname)
        else:
            prop["default"] = param.default
        properties[pname] = prop
    return {
        "type": "object",
        "properties": properties,
        "required": required,
    }


_PY_TO_JSON: dict[type, str] = {
    str: "string",
    int: "integer",
    float: "number",
    bool: "boolean",
    list: "array",
    dict: "object",
}


def _py_type(annotation: Any) -> str:
    if annotation is inspect.Parameter.empty:
        return "string"
    return _PY_TO_JSON.get(annotation, "string")


# ---------------------------------------------------------------------------
# Auth — Bearer token via X-MCP-Key. Disabled when MCP_AUTH_KEY is unset.
# ---------------------------------------------------------------------------
class BearerAuthMiddleware(BaseHTTPMiddleware):
    """Reject MCP requests without a valid X-MCP-Key header."""

    def __init__(self, app, key: str | None) -> None:
        super().__init__(app)
        self._key = key

    async def dispatch(
        self, request: Request, call_next: Callable[[Request], Awaitable[Response]]
    ) -> Response:
        if request.url.path in {"/healthz", "/livez", "/readyz"}:
            return await call_next(request)
        if self._key is None:
            return await call_next(request)
        provided = request.headers.get("X-MCP-Key")
        if provided != self._key:
            return JSONResponse({"error": "unauthorized"}, status_code=401)
        return await call_next(request)


# ---------------------------------------------------------------------------
# JSON-RPC handler — implements just the methods slm-router needs.
# ---------------------------------------------------------------------------
async def _jsonrpc_handler(request: Request) -> Response:
    """Plain JSON-RPC 2.0 over a single POST. Methods:

    - initialize       (returns serverInfo + capabilities)
    - tools/list       (returns the registered tools)
    - tools/call       (invokes a tool and returns its result)
    """
    if request.method != "POST":
        return JSONResponse(
            {"jsonrpc": "2.0", "error": {"code": -32600, "message": "POST only"}, "id": None},
            status_code=405,
        )
    try:
        body = await request.json()
    except Exception:
        return JSONResponse(
            {"jsonrpc": "2.0", "error": {"code": -32700, "message": "parse error"}, "id": None}
        )

    rpc_id = body.get("id")
    method = body.get("method")
    params = body.get("params") or {}

    # Capture the trusted conversation/customer context for this request.
    # Safe to trust: BearerAuthMiddleware already validated X-MCP-Key before
    # this handler ran. Only non-empty headers are kept (absent == unknown).
    # Header-supplied identity is authoritative over anything in tool args.
    request_ctx.set(
        {field: request.headers[h] for h, field in _TRUSTED_HEADERS.items() if request.headers.get(h)}
    )

    registry: ToolRegistry = request.app.state.registry

    try:
        if method == "initialize":
            cfg: Config = request.app.state.cfg
            result = {
                "protocolVersion": "2024-11-05",
                "serverInfo": {"name": f"{cfg.tenant}-mcp", "version": "0.1.0"},
                "capabilities": {"tools": {}},
            }
        elif method == "tools/list":
            result = {"tools": registry.list_tools()}
        elif method == "tools/call":
            try:
                output = await registry.call(params.get("name", ""), params.get("arguments") or {})
            except KeyError:
                return JSONResponse(
                    {
                        "jsonrpc": "2.0",
                        "id": rpc_id,
                        "error": {"code": -32601, "message": f"unknown tool: {params.get('name')}"},
                    }
                )
            # MCP tools/call response carries `content` (list of typed
            # payloads). slm-router accepts the structured payload too;
            # we put the dict result under both for compatibility.
            result = {
                "content": [{"type": "text", "text": json.dumps(output)}],
                "structuredContent": output,
                "isError": False,
            }
        else:
            return JSONResponse(
                {
                    "jsonrpc": "2.0",
                    "id": rpc_id,
                    "error": {"code": -32601, "message": f"unknown method: {method}"},
                }
            )
    except Exception as exc:  # pragma: no cover - defensive
        logger.exception("jsonrpc handler failed")
        return JSONResponse(
            {
                "jsonrpc": "2.0",
                "id": rpc_id,
                "error": {"code": -32603, "message": f"internal error: {exc}"},
            },
            status_code=500,
        )

    return JSONResponse({"jsonrpc": "2.0", "id": rpc_id, "result": result})


# ---------------------------------------------------------------------------
# App factory.
# ---------------------------------------------------------------------------
def build_registry(cfg: Config) -> ToolRegistry:
    reg = ToolRegistry(allow_mutations=cfg.allow_mutations)
    # shared_tools + tenants currently call mcp.tool(name=...,
    # description=...) on a FastMCP instance. The ToolRegistry above
    # exposes the same decorator surface, so both modules register
    # against it unchanged.
    shared_tools.register(reg, cfg)
    tenants.register(reg, cfg)
    # Auto-registered tools come LAST so a freshly-tagged OpenAPI op
    # can't accidentally shadow a hand-rolled tool with intentional
    # logic (e.g. a write tool that wraps a GET with confirmation).
    # `auto_tools.register` is intentionally synchronous — it runs
    # at module-import time, before uvicorn has finished spinning
    # up its event loop, and asyncio.run() can't be called when a
    # loop is already running (newer uvicorn does this).
    auto_count = auto_tools.register(reg, cfg)
    if auto_count:
        logger.info(
            "auto-registered %d openapi tool(s) for tenant=%s", auto_count, cfg.tenant
        )
    return reg


def build_app() -> Starlette:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    cfg = load()
    logger.info("starting mcp-gateway tenant=%s port=%s", cfg.tenant, cfg.bind_port)

    registry = build_registry(cfg)
    logger.info("registered %d tools for tenant=%s: %s",
                len(registry.list_tools()), cfg.tenant,
                [t["name"] for t in registry.list_tools()])

    async def healthz(_request: Request) -> Response:
        return JSONResponse({"ok": True, "tenant": cfg.tenant})

    routes = [
        Route("/healthz", healthz),
        Route("/livez", healthz),
        Route("/readyz", healthz),
        Route("/mcp", _jsonrpc_handler, methods=["POST"]),
    ]
    app = Starlette(routes=routes)
    app.add_middleware(BearerAuthMiddleware, key=cfg.auth_key)
    app.state.cfg = cfg
    app.state.registry = registry
    return app


# Module-level for `uvicorn mcp_gateway.server:app`.
app = build_app()


_ = re  # keep import for future schema generation
