from __future__ import annotations

import pytest

from mcp_gateway.config import load


def test_network_server_fails_closed_when_auth_is_not_configured(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    monkeypatch.delenv("MCP_AUTH_KEY", raising=False)
    monkeypatch.delenv("MCP_ALLOW_INSECURE_NO_AUTH", raising=False)

    with pytest.raises(SystemExit) as exc:
        load()

    assert exc.value.code == 2
