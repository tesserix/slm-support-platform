from __future__ import annotations

import asyncio
import dataclasses
import json
from typing import Any

import httpx
import pytest

from mcp_gateway.config import load
from mcp_gateway.server import build_runtime

PROTOCOL_VERSION = "2026-07-28"


class LifespanListener:
    def __init__(self) -> None:
        self.app: Any | None = None
        self._receive: asyncio.Queue[dict[str, object]] = asyncio.Queue()
        self._send: asyncio.Queue[dict[str, object]] = asyncio.Queue()
        self._task: asyncio.Task[None] | None = None

    @property
    def bound_port(self) -> int:
        return 8765

    async def start(self, app: Any, *, startup_timeout: float) -> None:
        self.app = app

        async def receive() -> dict[str, object]:
            return await self._receive.get()

        async def send(message: dict[str, object]) -> None:
            await self._send.put(message)

        self._task = asyncio.create_task(
            app(
                {
                    "type": "lifespan",
                    "asgi": {"version": "3.0", "spec_version": "2.0"},
                    "state": {},
                },
                receive,
                send,
            )
        )
        await self._receive.put({"type": "lifespan.startup"})
        message = await asyncio.wait_for(self._send.get(), timeout=startup_timeout)
        assert message.get("type") == "lifespan.startup.complete"

    async def stop(self) -> None:
        if self._task is None:
            return
        await self._receive.put({"type": "lifespan.shutdown"})
        message = await asyncio.wait_for(self._send.get(), timeout=2)
        assert message.get("type") == "lifespan.shutdown.complete"
        await self._task
        self._task = None


def _request(method: str, *, version: str = PROTOCOL_VERSION, name: str | None = None):
    params: dict[str, object] = {
        "_meta": {
            "io.modelcontextprotocol/protocolVersion": version,
            "io.modelcontextprotocol/clientCapabilities": {},
            "io.modelcontextprotocol/clientInfo": {"name": "protocol-test", "version": "1"},
        }
    }
    if name is not None:
        params.update({"name": name, "arguments": {}})
    headers = {
        "MCP-Protocol-Version": version,
        "MCP-Method": method,
        "Accept": "application/json, text/event-stream",
    }
    if name is not None:
        headers["MCP-Name"] = name
    return {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}, headers


@pytest.fixture
async def client():
    listener = LifespanListener()
    cfg = dataclasses.replace(
        load(),
        bind_host="127.0.0.1",
        auth_key="test-secret",
        allowed_hosts=(),
        allowed_origins=(),
    )
    runtime = build_runtime(cfg, listener=listener)
    await runtime.start()
    assert listener.app is not None
    async with httpx.AsyncClient(
        transport=httpx.ASGITransport(app=listener.app),
        base_url="http://127.0.0.1:8765",
    ) as http_client:
        yield http_client
    await runtime.stop()


@pytest.mark.asyncio
async def test_modern_requests_are_discoverable_and_stateless(client: httpx.AsyncClient) -> None:
    body, headers = _request("server/discover")
    headers["X-MCP-Key"] = "test-secret"
    discovered = await client.post("/mcp", json=body, headers=headers)

    assert discovered.status_code == 200
    assert discovered.json()["result"]["supportedVersions"] == [PROTOCOL_VERSION]
    assert "Mcp-Session-Id" not in discovered.headers

    body, headers = _request("tools/list")
    headers["X-MCP-Key"] = "test-secret"
    listed = await client.post("/mcp", json=body, headers=headers)
    assert listed.status_code == 200
    assert listed.json()["result"]["tools"]


@pytest.mark.asyncio
async def test_authentication_happens_before_body_parsing(client: httpx.AsyncClient) -> None:
    response = await client.post(
        "/mcp",
        content=b"not-json",
        headers={"MCP-Protocol-Version": PROTOCOL_VERSION},
    )

    assert response.status_code == 401
    assert b"not-json" not in response.content


@pytest.mark.asyncio
async def test_modern_request_validation_fails_closed(client: httpx.AsyncClient) -> None:
    body, headers = _request("server/discover", version="1900-01-01")
    headers["X-MCP-Key"] = "test-secret"
    unsupported = await client.post("/mcp", json=body, headers=headers)
    assert unsupported.status_code == 400

    body, headers = _request("tools/list")
    headers.update({"X-MCP-Key": "test-secret", "Mcp-Session-Id": "forbidden"})
    session = await client.post("/mcp", json=body, headers=headers)
    assert session.status_code == 404


@pytest.mark.asyncio
async def test_tools_call_requires_matching_tenant_context(client: httpx.AsyncClient) -> None:
    body, headers = _request("tools/call", name="get_order")
    headers.update({"X-MCP-Key": "test-secret", "X-Tenant-Id": "homechef"})
    response = await client.post("/mcp", json=body, headers=headers)

    assert response.status_code == 401


@pytest.mark.asyncio
async def test_tools_call_preserves_structured_product_result(client: httpx.AsyncClient) -> None:
    body, headers = _request("tools/call", name="search_knowledge_base")
    body["params"]["arguments"] = {"query": "refund policy"}
    headers.update({"X-MCP-Key": "test-secret", "X-Tenant-Id": "mark8ly"})
    response = await client.post("/mcp", json=body, headers=headers)

    assert response.status_code == 200
    result = response.json()["result"]
    assert result["isError"] is False
    assert result["structuredContent"]["tenant"] == "mark8ly"
    assert result["structuredContent"]["query"] == "refund policy"


@pytest.mark.asyncio
async def test_operational_routes_do_not_require_mcp_credentials(client: httpx.AsyncClient) -> None:
    assert (await client.get("/livez")).status_code == 200
    assert (await client.get("/readyz")).status_code == 200
    assert (await client.get("/metrics")).status_code == 200


@pytest.mark.asyncio
async def test_independent_calls_can_use_separate_runtime_instances() -> None:
    async def list_from_new_instance() -> list[str]:
        listener = LifespanListener()
        cfg = dataclasses.replace(
            load(),
            bind_host="127.0.0.1",
            auth_key="test-secret",
            allowed_hosts=(),
            allowed_origins=(),
        )
        runtime = build_runtime(cfg, listener=listener)
        await runtime.start()
        assert listener.app is not None
        try:
            async with httpx.AsyncClient(
                transport=httpx.ASGITransport(app=listener.app),
                base_url="http://127.0.0.1:8765",
            ) as http_client:
                body, headers = _request("tools/list")
                headers["X-MCP-Key"] = "test-secret"
                response = await http_client.post("/mcp", json=body, headers=headers)
                assert response.status_code == 200
                return [tool["name"] for tool in response.json()["result"]["tools"]]
        finally:
            await runtime.stop()

    assert await list_from_new_instance() == await list_from_new_instance()


@pytest.mark.asyncio
async def test_request_body_is_bounded(client: httpx.AsyncClient) -> None:
    response = await client.post(
        "/mcp",
        content=json.dumps({"padding": "x" * 70_000}),
        headers={
            "X-MCP-Key": "test-secret",
            "Content-Type": "application/json",
            "MCP-Protocol-Version": PROTOCOL_VERSION,
            "MCP-Method": "tools/list",
        },
    )
    assert response.status_code == 413
