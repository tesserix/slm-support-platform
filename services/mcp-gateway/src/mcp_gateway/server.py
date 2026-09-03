"""Stateless MCP 2026-07-28 server for per-product support tools."""

from __future__ import annotations

import contextvars
import hmac
import inspect
import json
import logging
import re
from collections.abc import Awaitable, Callable
from typing import Any

from starlette.applications import Starlette
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Route

from . import auto_tools, shared_tools, tenants
from .config import Config, load

logger = logging.getLogger(__name__)

_PROTOCOL_VERSION = "2026-07-28"
_MAX_REQUEST_BYTES = 1 << 20

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
request_ctx: contextvars.ContextVar[dict[str, str] | None] = contextvars.ContextVar(
    "mcp_request_ctx", default=None
)


# ---------------------------------------------------------------------------
# Tool registry — what every transport shares.
# ---------------------------------------------------------------------------
class ToolRegistry:
    """A small tool registry that emits
    JSON-RPC-shaped responses on demand. Tools are async callables.
    """

    def __init__(self) -> None:
        self._tools: dict[str, dict[str, Any]] = {}

    def tool(
        self, *, name: str, description: str = ""
    ) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        def decorator(fn: Callable[..., Any]) -> Callable[..., Any]:
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
        if provided is None or not hmac.compare_digest(provided, self._key):
            return JSONResponse({"error": "unauthorized"}, status_code=401)
        return await call_next(request)


# ---------------------------------------------------------------------------
# JSON-RPC handler — every request is independently complete.
# ---------------------------------------------------------------------------
def _rpc_error(rpc_id: Any, code: int, message: str, status_code: int) -> JSONResponse:
    return JSONResponse(
        {"jsonrpc": "2.0", "id": rpc_id, "error": {"code": code, "message": message}},
        status_code=status_code,
    )


async def _jsonrpc_handler(request: Request) -> Response:
    """Serve discovery and tool calls without sessions or connection affinity."""
    if request.headers.get("Mcp-Session-Id"):
        return _rpc_error(None, -32600, "invalid session", 404)

    content_length = request.headers.get("Content-Length")
    if content_length and content_length.isdigit() and int(content_length) > _MAX_REQUEST_BYTES:
        return _rpc_error(None, -32600, "request too large", 413)

    try:
        raw_body = await request.body()
        if len(raw_body) > _MAX_REQUEST_BYTES:
            return _rpc_error(None, -32600, "request too large", 413)
        body = json.loads(raw_body)
    except (json.JSONDecodeError, UnicodeDecodeError):
        return _rpc_error(None, -32700, "parse error", 400)

    if not isinstance(body, dict):
        return _rpc_error(None, -32600, "invalid request", 400)

    rpc_id = body.get("id")
    method = body.get("method")
    params = body.get("params") or {}
    if not isinstance(method, str) or not isinstance(params, dict):
        return _rpc_error(rpc_id, -32600, "invalid request", 400)

    header_version = request.headers.get("MCP-Protocol-Version", "")
    if header_version != _PROTOCOL_VERSION:
        return _rpc_error(rpc_id, -32022, f"unsupported protocol version: {header_version}", 400)

    if request.headers.get("MCP-Method", "") != method:
        return _rpc_error(rpc_id, -32020, "MCP-Method does not match JSON-RPC method", 400)

    metadata = params.get("_meta")
    if not isinstance(metadata, dict):
        return _rpc_error(rpc_id, -32602, "missing request metadata", 400)
    if metadata.get("io.modelcontextprotocol/protocolVersion") != header_version:
        return _rpc_error(rpc_id, -32020, "protocol metadata does not match header", 400)
    if not isinstance(metadata.get("io.modelcontextprotocol/clientCapabilities"), dict):
        return _rpc_error(rpc_id, -32602, "missing client capabilities", 400)

    if method == "tools/call" and request.headers.get("MCP-Name", "") != params.get("name"):
        return _rpc_error(rpc_id, -32020, "MCP-Name does not match tool name", 400)

    cfg: Config = request.app.state.cfg
    if method == "tools/call" and request.headers.get("X-Tenant-Id", "") != cfg.tenant:
        return _rpc_error(rpc_id, -32001, "tenant context does not match MCP server", 403)

    request_ctx.set(
        {
            field: request.headers[h]
            for h, field in _TRUSTED_HEADERS.items()
            if request.headers.get(h)
        }
    )
    registry: ToolRegistry = request.app.state.registry

    try:
        if method == "server/discover":
            result = {
                "supportedVersions": [_PROTOCOL_VERSION],
                "serverInfo": {"name": f"{cfg.tenant}-mcp", "version": "0.1.0"},
                "capabilities": {"tools": {}},
            }
        elif method == "tools/list":
            result = {"tools": registry.list_tools()}
        elif method == "tools/call":
            try:
                output = await registry.call(params.get("name", ""), params.get("arguments") or {})
            except KeyError:
                return _rpc_error(rpc_id, -32601, f"unknown tool: {params.get('name')}", 200)
            result = {
                "content": [{"type": "text", "text": json.dumps(output)}],
                "structuredContent": output,
                "isError": False,
            }
        else:
            return _rpc_error(rpc_id, -32601, f"unknown method: {method}", 404)
    except Exception as exc:  # pragma: no cover - defensive
        logger.exception("jsonrpc handler failed")
        return _rpc_error(rpc_id, -32603, f"internal error: {exc}", 500)

    return JSONResponse({"jsonrpc": "2.0", "id": rpc_id, "result": result})


# ---------------------------------------------------------------------------
# App factory.
# ---------------------------------------------------------------------------
def build_registry(cfg: Config) -> ToolRegistry:
    reg = ToolRegistry()
    # shared_tools + tenants register against this decorator surface.
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
        logger.info("auto-registered %d openapi tool(s) for tenant=%s", auto_count, cfg.tenant)
    return reg


def build_app() -> Starlette:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    cfg = load()
    logger.info("starting mcp-gateway tenant=%s port=%s", cfg.tenant, cfg.bind_port)

    registry = build_registry(cfg)
    logger.info(
        "registered %d tools for tenant=%s: %s",
        len(registry.list_tools()),
        cfg.tenant,
        [t["name"] for t in registry.list_tools()],
    )

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
