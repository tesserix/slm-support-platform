"""FastMCP server wiring.

Builds the singleton ``FastMCP`` instance, registers the shared +
tenant-specific tool groups, and exposes a Starlette ASGI app at
``app`` that mounts the MCP Streamable-HTTP transport at ``/mcp``
plus the standard k8s probe endpoints.

Bearer-token auth via ``X-MCP-Key`` is on whenever ``MCP_AUTH_KEY``
is set (production). In dev MCP_AUTH_KEY is unset and the server
runs open inside the namespace's network policy.
"""
from __future__ import annotations

import logging
from typing import Any, Awaitable, Callable

from mcp.server.fastmcp import FastMCP
from starlette.applications import Starlette
from starlette.middleware.base import BaseHTTPMiddleware
from starlette.requests import Request
from starlette.responses import JSONResponse, Response
from starlette.routing import Mount, Route

from .config import Config, load
from . import shared_tools, tenants

logger = logging.getLogger(__name__)


def build_mcp(cfg: Config) -> FastMCP:
    """Build the FastMCP server with the tenant's tool set."""
    mcp = FastMCP(
        name=f"{cfg.tenant}-mcp",
        instructions=(
            f"Tools for the {cfg.tenant} product. Tools marked '_stub: true' "
            "return realistic sample data — caveat any answer that quotes them "
            "as based on a sample response rather than the live system."
        ),
    )
    shared_tools.register(mcp, cfg)
    tenants.register(mcp, cfg)
    return mcp


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


def build_app() -> Starlette:
    logging.basicConfig(
        level=logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )
    cfg = load()
    logger.info("starting mcp-gateway tenant=%s port=%s", cfg.tenant, cfg.bind_port)

    mcp = build_mcp(cfg)
    mcp_app = mcp.streamable_http_app()

    async def healthz(_request: Request) -> Response:
        return JSONResponse({"ok": True, "tenant": cfg.tenant})

    routes: list[Any] = [
        Route("/healthz", healthz),
        Route("/livez", healthz),
        Route("/readyz", healthz),
        Mount("/mcp", app=mcp_app),
    ]

    app = Starlette(routes=routes)
    app.add_middleware(BearerAuthMiddleware, key=cfg.auth_key)
    app.state.cfg = cfg
    return app


# Module-level for `uvicorn mcp_gateway.server:app`.
app = build_app()
