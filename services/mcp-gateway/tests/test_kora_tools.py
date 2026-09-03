from __future__ import annotations

import dataclasses

import httpx
import respx

from mcp_gateway import tenants
from mcp_gateway.config import load
from mcp_gateway.server import ToolRegistry


def _registry(**overrides: object) -> ToolRegistry:
    cfg = dataclasses.replace(load(), tenant="kora", kora_api_url="https://kora.test", **overrides)
    registry = ToolRegistry(allow_mutations=False)
    tenants.register(registry, cfg)
    return registry


@respx.mock
async def test_search_nutrition_uses_internal_route_with_key() -> None:
    registry = _registry(kora_internal_key="service-key")
    route = respx.get("https://kora.test/internal/v1/foods").mock(
        return_value=httpx.Response(200, json={"foods": []})
    )

    result = await registry.call("search_nutrition", {"query": "apple"})

    assert result == {"foods": [], "source": "kora-nutrition"}
    request = route.calls.last.request
    assert request.headers["X-Internal-Key"] == "service-key"
    assert request.url.params["q"] == "apple"


@respx.mock
async def test_search_nutrition_falls_back_to_user_route_without_key() -> None:
    registry = _registry(kora_internal_key=None)
    route = respx.get("https://kora.test/v1/foods").mock(
        return_value=httpx.Response(200, json={"foods": []})
    )

    await registry.call("search_nutrition", {"query": "apple"})

    assert "X-Internal-Key" not in route.calls.last.request.headers
