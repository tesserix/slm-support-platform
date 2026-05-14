"""Tests for auto_tools — OpenAPI operations → MCP tool registrations."""
from __future__ import annotations

import dataclasses

import httpx
import pytest
import respx

from mcp_gateway import auto_tools
from mcp_gateway.openapi_loader import (
    EXPOSE_CUSTOMER_READ,
    MCP_EXPOSE_EXTENSION,
)
from mcp_gateway.server import ToolRegistry


@dataclasses.dataclass
class _FakeCfg:
    """Minimal stand-in for Config that only carries the two fields
    auto_tools.register cares about. Not frozen because dict-valued
    fields make hash() unstable — these tests don't need it either way."""
    openapi_urls: tuple[str, ...]
    openapi_backend_headers: dict = dataclasses.field(default_factory=dict)


def _spec_with_get_order() -> dict:
    return {
        "openapi": "3.0.3",
        "info": {"title": "fake", "version": "0"},
        "servers": [{"url": "https://api.fake.test/v1"}],
        "paths": {
            "/orders/{order_id}": {
                "get": {
                    "operationId": "getOrder",
                    "summary": "Look up an order",
                    "description": "Returns full order detail.",
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "parameters": [
                        {
                            "name": "order_id",
                            "in": "path",
                            "required": True,
                            "schema": {"type": "string"},
                        },
                        {
                            "name": "include_items",
                            "in": "query",
                            "schema": {"type": "boolean", "default": False},
                        },
                    ],
                }
            }
        },
    }


# -----------------------------------------------------------------------------
# Startup robustness — empty config / unreachable backend.
# -----------------------------------------------------------------------------
async def test_no_urls_registers_nothing() -> None:
    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=())
    count = await auto_tools.register(reg, cfg)
    assert count == 0
    assert reg.list_tools() == []


@respx.mock
async def test_unreachable_backend_is_swallowed() -> None:
    """One dead backend must not stop the others; pod startup is on
    the customer's hot path."""
    respx.get("https://dead.test/openapi.json").mock(
        side_effect=httpx.ConnectError("nope")
    )
    respx.get("https://alive.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )

    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://dead.test/openapi.json", "https://alive.test/openapi.json"))
    count = await auto_tools.register(reg, cfg)
    assert count == 1
    assert reg.list_tools()[0]["name"] == "getOrder"


# -----------------------------------------------------------------------------
# Tool registration shape.
# -----------------------------------------------------------------------------
@respx.mock
async def test_registered_tool_has_openapi_derived_schema() -> None:
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))

    await auto_tools.register(reg, cfg)

    tools = reg.list_tools()
    assert len(tools) == 1
    entry = tools[0]
    assert entry["name"] == "getOrder"
    schema = entry["inputSchema"]
    assert schema["type"] == "object"
    assert "order_id" in schema["properties"]
    assert "include_items" in schema["properties"]
    # Path param is mandatory; optional query param is not.
    assert schema["required"] == ["order_id"]
    # Default from the OpenAPI schema is preserved so the SLM knows
    # the parameter is optional and what shape to fill.
    assert schema["properties"]["include_items"]["default"] is False


@respx.mock
async def test_description_combines_summary_and_description() -> None:
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)
    desc = reg.list_tools()[0]["description"]
    assert "Look up an order" in desc
    assert "Returns full order detail" in desc


# -----------------------------------------------------------------------------
# Call-time behaviour — substituting path params, forwarding query.
# -----------------------------------------------------------------------------
@respx.mock
async def test_tool_substitutes_path_params_and_forwards_query() -> None:
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    route = respx.get(
        "https://api.fake.test/v1/orders/ord_42",
        params={"include_items": "true"},
    ).mock(return_value=httpx.Response(200, json={"id": "ord_42", "total": 99.5}))

    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)

    result = await reg.call("getOrder", {"order_id": "ord_42", "include_items": True})
    assert route.called
    assert result["id"] == "ord_42"
    # The auto-tool annotates the response so the SLM can audit which
    # endpoint produced the value.
    assert result["_operation_id"] == "getOrder"


@respx.mock
async def test_tool_missing_path_param_returns_structured_error() -> None:
    """Don't let the SLM silently send a literal `{order_id}` to the
    backend if it forgets to fill the param — return a structured
    error so the SLM can ask the customer for the missing piece."""
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)

    result = await reg.call("getOrder", {})
    assert result["error"] == "missing_argument"
    assert "order_id" in result["detail"]


@respx.mock
async def test_tool_backend_unreachable_returns_structured_error() -> None:
    """Network failures at call time mirror startup failures —
    structured error, never an exception."""
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    respx.get("https://api.fake.test/v1/orders/ord_42").mock(
        side_effect=httpx.ConnectError("backend down")
    )

    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)

    result = await reg.call("getOrder", {"order_id": "ord_42"})
    assert result["error"] == "backend_unreachable"


@respx.mock
async def test_tool_forwards_static_backend_headers() -> None:
    """When the gateway's config has `openapi_backend_headers`, the
    auto-tool must send them on every backend call — that's how
    shared-secret headers like X-Storefront-Key get propagated to
    the backend without exposing them to the SLM."""
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    route = respx.get("https://api.fake.test/v1/orders/ord_42").mock(
        return_value=httpx.Response(200, json={"id": "ord_42"})
    )

    reg = ToolRegistry()
    cfg = _FakeCfg(
        openapi_urls=("https://api.fake.test/openapi.json",),
        openapi_backend_headers={"X-Storefront-Key": "shh", "X-Service": "mcp"},
    )
    await auto_tools.register(reg, cfg)
    await reg.call("getOrder", {"order_id": "ord_42"})

    assert route.called
    sent_headers = route.calls.last.request.headers
    assert sent_headers["X-Storefront-Key"] == "shh"
    assert sent_headers["X-Service"] == "mcp"


@respx.mock
async def test_tool_does_not_send_headers_when_none_configured() -> None:
    """The default (no configured headers) preserves the original
    behaviour — bare GET with no extras. Important for backends like
    fanzone-user that the SLM has been calling header-less for months."""
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    route = respx.get("https://api.fake.test/v1/orders/ord_42").mock(
        return_value=httpx.Response(200, json={"id": "ord_42"})
    )

    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)
    await reg.call("getOrder", {"order_id": "ord_42"})

    assert route.called
    sent_headers = route.calls.last.request.headers
    # httpx always sends host / user-agent / accept — we just want
    # to make sure no surprise auth-ish headers slipped in.
    assert "X-Storefront-Key" not in sent_headers
    assert "Authorization" not in sent_headers


@respx.mock
async def test_tool_backend_4xx_returns_lookup_failed() -> None:
    respx.get("https://api.fake.test/openapi.json").mock(
        return_value=httpx.Response(200, json=_spec_with_get_order())
    )
    respx.get("https://api.fake.test/v1/orders/missing").mock(
        return_value=httpx.Response(404, json={"error": "not_found"})
    )

    reg = ToolRegistry()
    cfg = _FakeCfg(openapi_urls=("https://api.fake.test/openapi.json",))
    await auto_tools.register(reg, cfg)

    result = await reg.call("getOrder", {"order_id": "missing"})
    assert result["error"] == "lookup_failed"
    assert result["status"] == 404
