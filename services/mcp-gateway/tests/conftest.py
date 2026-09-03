"""Environment defaults shared by the gateway tests."""

from __future__ import annotations

import os

# Set BEFORE any mcp_gateway import. Pytest reads conftest.py first,
# so this runs before the `from mcp_gateway...` lines in test modules.
os.environ.setdefault("MCP_TENANT", "mark8ly")
os.environ.setdefault("MCP_ALLOW_INSECURE_NO_AUTH", "true")
# Don't auto-load any OpenAPI URLs in unit tests — every test that
# needs them sets up its own respx mocks and passes the URLs through
# a fake config.
os.environ.setdefault("MCP_OPENAPI_URLS", "")
