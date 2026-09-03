from __future__ import annotations

import dataclasses

import httpx
import respx

from mcp_gateway import tenants
from mcp_gateway.config import load
from mcp_gateway.server import ToolRegistry


@respx.mock
async def test_contact_lead_matches_tesserix_home_contract() -> None:
    cfg = dataclasses.replace(load(), tenant="platform", tesserix_home_url="https://platform.test")
    registry = ToolRegistry()
    tenants.register(registry, cfg)
    route = respx.post("https://platform.test/api/contact").mock(
        return_value=httpx.Response(200, json={"success": True})
    )

    result = await registry.call(
        "submit_contact_lead",
        {
            "name": "Ada Lovelace",
            "email": "ada@example.test",
            "message": "Please arrange a demo",
            "company": "Analytical Engines",
        },
    )

    assert result["success"] is True
    assert route.calls.last.request.content == (
        b'{"firstName":"Ada","lastName":"Lovelace","email":"ada@example.test",'
        b'"message":"Please arrange a demo","company":"Analytical Engines",'
        b'"interest":"demo"}'
    )
