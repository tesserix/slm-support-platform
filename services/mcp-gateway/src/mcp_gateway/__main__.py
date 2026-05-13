"""`python -m mcp_gateway` entrypoint — uvicorn run wrapper."""
from __future__ import annotations

import os

import uvicorn


def main() -> None:
    uvicorn.run(
        "mcp_gateway.server:app",
        host=os.environ.get("MCP_HOST", "0.0.0.0"),
        port=int(os.environ.get("MCP_PORT", "8765")),
        log_level=os.environ.get("MCP_LOG_LEVEL", "info").lower(),
        access_log=True,
    )


if __name__ == "__main__":
    main()
