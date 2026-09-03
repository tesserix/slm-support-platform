from __future__ import annotations

from starlette.testclient import TestClient

from mcp_gateway.server import app

PROTOCOL_VERSION = "2026-07-28"


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


def test_modern_requests_are_discoverable_and_stateless() -> None:
    with TestClient(app) as client:
        body, headers = _request("server/discover")
        discovered = client.post("/mcp", json=body, headers=headers)

        assert discovered.status_code == 200
        assert discovered.json()["result"]["supportedVersions"] == [PROTOCOL_VERSION]
        assert "Mcp-Session-Id" not in discovered.headers

        body, headers = _request("tools/list")
        listed = client.post("/mcp", json=body, headers=headers)
        assert listed.status_code == 200
        assert listed.json()["result"]["tools"]


def test_modern_request_validation_fails_closed() -> None:
    with TestClient(app) as client:
        body, headers = _request("server/discover", version="1900-01-01")
        unsupported = client.post("/mcp", json=body, headers=headers)
        assert unsupported.status_code == 400
        assert unsupported.json()["error"]["code"] == -32022

        body, headers = _request("tools/list")
        headers["MCP-Method"] = "tools/call"
        mismatch = client.post("/mcp", json=body, headers=headers)
        assert mismatch.status_code == 400
        assert mismatch.json()["error"]["code"] == -32020

        body, headers = _request("tools/list")
        headers["Mcp-Session-Id"] = "forbidden"
        session = client.post("/mcp", json=body, headers=headers)
        assert session.status_code == 404

        body, headers = _request("subscriptions/listen")
        unknown = client.post("/mcp", json=body, headers=headers)
        assert unknown.status_code == 404
        assert unknown.json()["error"]["code"] == -32601

        body, headers = _request("initialize")
        legacy = client.post("/mcp", json=body, headers=headers)
        assert legacy.status_code == 404
        assert legacy.json()["error"]["code"] == -32601


def test_tools_call_requires_matching_name_header() -> None:
    with TestClient(app) as client:
        body, headers = _request("tools/call", name="get_order")
        headers["MCP-Name"] = "other-tool"
        response = client.post("/mcp", json=body, headers=headers)

    assert response.status_code == 400
    assert response.json()["error"]["code"] == -32020


def test_tools_call_requires_matching_tenant_context() -> None:
    with TestClient(app) as client:
        body, headers = _request("tools/call", name="get_order")
        headers["X-Tenant-Id"] = "homechef"
        response = client.post("/mcp", json=body, headers=headers)

    assert response.status_code == 403
    assert response.json()["error"]["code"] == -32001


def test_get_and_oversized_requests_are_rejected() -> None:
    with TestClient(app) as client:
        assert client.get("/mcp").status_code == 405
        oversized = client.post(
            "/mcp",
            content=b"x" * ((1 << 20) + 1),
            headers={
                "Content-Type": "application/json",
                "MCP-Protocol-Version": PROTOCOL_VERSION,
                "MCP-Method": "tools/list",
            },
        )

    assert oversized.status_code == 413
