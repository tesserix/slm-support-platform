from __future__ import annotations

import pytest

from mcp_gateway.config import load


@pytest.mark.parametrize(
    ("tenant", "namespace"),
    [("homechef", "homechef"), ("platform", "support-platform")],
)
def test_default_allowed_hosts_accept_gateway_hostname_rewrites(
    monkeypatch: pytest.MonkeyPatch,
    tenant: str,
    namespace: str,
) -> None:
    monkeypatch.setenv("MCP_TENANT", tenant)
    monkeypatch.setenv("MCP_AUTH_KEY", "test-key")
    monkeypatch.setenv("MCP_PORT", "8765")
    monkeypatch.delenv("MCP_ALLOWED_HOSTS", raising=False)

    config = load()

    service = f"{tenant}-mcp"
    fqdn = f"{service}.{namespace}.svc.cluster.local"
    assert config.allowed_hosts == (
        service,
        f"{service}:8765",
        fqdn,
        f"{fqdn}:8765",
    )
