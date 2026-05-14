"""Tests for the OpenAPI spec loader.

Covers the fail-soft contract — every plausible bad-spec scenario must
yield zero exposed operations and a logged warning, never raise. The
mcp-gateway pod must start cleanly even when a backend is offline or
serving garbage.
"""
from __future__ import annotations

import json

import httpx
import pytest
import respx

from mcp_gateway.openapi_loader import (
    EXPOSE_CUSTOMER_READ,
    MCP_EXPOSE_EXTENSION,
    extract_exposed_operations,
    fetch_spec,
)


def _minimal_spec(paths: dict) -> dict:
    return {"openapi": "3.0.3", "info": {"title": "t", "version": "0"}, "paths": paths}


# -----------------------------------------------------------------------------
# fetch_spec — network / parse / shape failures.
# -----------------------------------------------------------------------------
@respx.mock
async def test_fetch_spec_network_error_returns_none() -> None:
    respx.get("https://api.test/openapi.json").mock(
        side_effect=httpx.ConnectError("backend down")
    )
    assert await fetch_spec("https://api.test/openapi.json") is None


@respx.mock
async def test_fetch_spec_non_200_returns_none() -> None:
    respx.get("https://api.test/openapi.json").mock(return_value=httpx.Response(503))
    assert await fetch_spec("https://api.test/openapi.json") is None


@respx.mock
async def test_fetch_spec_non_json_body_returns_none() -> None:
    respx.get("https://api.test/openapi.json").mock(
        return_value=httpx.Response(200, text="<html>oops</html>")
    )
    assert await fetch_spec("https://api.test/openapi.json") is None


@respx.mock
async def test_fetch_spec_wrong_shape_returns_none() -> None:
    """JSON parses but is missing the `paths` key — not a valid spec."""
    respx.get("https://api.test/openapi.json").mock(
        return_value=httpx.Response(200, json={"openapi": "3.0.0", "info": {}})
    )
    assert await fetch_spec("https://api.test/openapi.json") is None


@respx.mock
async def test_fetch_spec_swagger_2_rejected() -> None:
    """Swagger 2.0 docs have a different structure (no `openapi` field).
    We don't auto-translate; the backend must serve OpenAPI 3.x."""
    respx.get("https://api.test/openapi.json").mock(
        return_value=httpx.Response(
            200, json={"swagger": "2.0", "paths": {"/foo": {}}}
        )
    )
    assert await fetch_spec("https://api.test/openapi.json") is None


@respx.mock
async def test_fetch_spec_happy_path() -> None:
    spec = _minimal_spec({"/foo": {}})
    respx.get("https://api.test/openapi.json").mock(
        return_value=httpx.Response(200, json=spec)
    )
    out = await fetch_spec("https://api.test/openapi.json")
    assert out == spec


# -----------------------------------------------------------------------------
# extract_exposed_operations — filtering, schema preservation.
# -----------------------------------------------------------------------------
def test_extract_skips_operations_without_extension() -> None:
    spec = _minimal_spec(
        {
            "/orders/{id}": {
                "get": {
                    "operationId": "getOrder",
                    "summary": "Look up an order",
                    "parameters": [
                        {"name": "id", "in": "path", "required": True, "schema": {"type": "string"}},
                    ],
                    # No x-mcp-expose tag => not eligible.
                }
            }
        }
    )
    assert extract_exposed_operations(spec) == []


def test_extract_skips_operations_with_wrong_extension_value() -> None:
    """Belt-and-braces: only the literal 'customer-read' opts in.
    Stray values like 'true' or 'public' must NOT auto-expose, since
    the whole point of the allowlist is engineers being explicit."""
    spec = _minimal_spec(
        {
            "/x": {
                "get": {
                    "operationId": "x",
                    MCP_EXPOSE_EXTENSION: "true",
                    "parameters": [],
                }
            }
        }
    )
    assert extract_exposed_operations(spec) == []


def test_extract_skips_non_get_operations() -> None:
    """Write operations need a separate confirmation flow we haven't
    built — Phase 1 is read-only."""
    spec = _minimal_spec(
        {
            "/orders": {
                "post": {
                    "operationId": "placeOrder",
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "parameters": [],
                }
            }
        }
    )
    assert extract_exposed_operations(spec) == []


def test_extract_skips_operations_without_operation_id() -> None:
    """No operationId → no tool name → not registrable."""
    spec = _minimal_spec(
        {
            "/orders": {
                "get": {
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "summary": "List orders",
                    "parameters": [],
                }
            }
        }
    )
    assert extract_exposed_operations(spec) == []


def test_extract_merges_path_level_parameters() -> None:
    """OpenAPI lets you declare params once at the path level and apply
    them to every operation under that path. Make sure the auto-loader
    sees those in addition to the operation-level params."""
    spec = _minimal_spec(
        {
            "/users/{user_id}/orders/{order_id}": {
                "parameters": [
                    {"name": "user_id", "in": "path", "required": True, "schema": {"type": "string"}},
                ],
                "get": {
                    "operationId": "getOrder",
                    "summary": "Order detail",
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "parameters": [
                        {"name": "order_id", "in": "path", "required": True, "schema": {"type": "string"}},
                    ],
                },
            }
        }
    )
    ops = extract_exposed_operations(spec)
    assert len(ops) == 1
    names = {p["name"] for p in ops[0].parameters}
    assert names == {"user_id", "order_id"}


def test_extract_happy_path_carries_base_url() -> None:
    spec = _minimal_spec(
        {
            "/orders/{order_id}": {
                "get": {
                    "operationId": "getOrder",
                    "summary": "Look up an order",
                    "description": "Returns status, items, totals.",
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "parameters": [
                        {"name": "order_id", "in": "path", "required": True, "schema": {"type": "string"}},
                    ],
                }
            }
        }
    )
    ops = extract_exposed_operations(spec, base_url="https://api.test")
    assert len(ops) == 1
    op = ops[0]
    assert op.operation_id == "getOrder"
    assert op.method == "GET"
    assert op.path == "/orders/{order_id}"
    assert op.base_url == "https://api.test"
    assert op.summary == "Look up an order"


def test_extract_handles_missing_paths_dict() -> None:
    """A spec where `paths` is None (some generators emit this for an
    empty surface) must yield zero ops, not crash."""
    out = extract_exposed_operations({"openapi": "3.0.0", "paths": None})
    assert out == []


def test_extract_handles_non_dict_path_item() -> None:
    """Defensive — if a generator emits `paths: {"/foo": "bad"}` we
    skip the entry rather than blow up."""
    spec = {"openapi": "3.0.0", "paths": {"/foo": "not-a-dict"}}
    assert extract_exposed_operations(spec) == []


@pytest.mark.parametrize("value", [None, "", "  "])
def test_extract_optional_summary_defaults_to_empty(value) -> None:
    spec = _minimal_spec(
        {
            "/x": {
                "get": {
                    "operationId": "getX",
                    "summary": value,
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "parameters": [],
                }
            }
        }
    )
    ops = extract_exposed_operations(spec)
    assert len(ops) == 1
    assert ops[0].summary == ""


def test_extract_json_roundtrip_is_stable() -> None:
    """Spec docs travel over the network as JSON. Make sure passing
    through json.dumps / json.loads doesn't change what we extract."""
    spec = _minimal_spec(
        {
            "/orders": {
                "get": {
                    "operationId": "listOrders",
                    MCP_EXPOSE_EXTENSION: EXPOSE_CUSTOMER_READ,
                    "summary": "Recent orders",
                    "parameters": [
                        {"name": "limit", "in": "query", "schema": {"type": "integer"}},
                    ],
                }
            }
        }
    )
    roundtripped = json.loads(json.dumps(spec))
    assert extract_exposed_operations(spec) == extract_exposed_operations(roundtripped)
