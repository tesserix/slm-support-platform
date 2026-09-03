"""Pytest fixtures + module-level setup.

The mcp-gateway's `server.py` builds the Starlette app at import time
so `uvicorn mcp_gateway.server:app` works without a factory call. That
in turn calls `config.load()` which `sys.exit(2)`s when `MCP_TENANT`
isn't set. Pin a tenant before any test module imports anything so
collection doesn't blow up.
"""

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
