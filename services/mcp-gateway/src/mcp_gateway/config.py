"""Runtime config for mcp-gateway."""
from __future__ import annotations

import dataclasses
import os
import sys


# Every supported tenant slug — must match the slm-router tenant ids
# and the @tesserix/otto-widget tenantId passed from each product
# frontend.
SUPPORTED_TENANTS: frozenset[str] = frozenset(
    {
        "mark8ly",
        "fanzone",
        "homechef",
        "stockpilot",
        "gameverse",
        "horoscope",
        "scrapper",
    }
)


@dataclasses.dataclass(frozen=True)
class Config:
    tenant: str
    bind_host: str
    bind_port: int
    # Bearer token clients send via X-MCP-Key. None disables auth (dev only).
    auth_key: str | None
    # Pgvector DSN used by the shared search_knowledge_base tool. Optional;
    # the tool returns "unavailable" if unset.
    vector_db_dsn: str | None
    # Embedder URL used to embed the search_knowledge_base query.
    embedder_url: str | None
    # Mongo URL used by the lookup_conversation tool. Same shape as
    # slm-router's MONGO_URI.
    mongo_url: str | None
    mongo_db: str


def load() -> Config:
    tenant = os.environ.get("MCP_TENANT", "").strip().lower()
    if tenant not in SUPPORTED_TENANTS:
        print(
            f"MCP_TENANT must be one of {sorted(SUPPORTED_TENANTS)} (got {tenant!r})",
            file=sys.stderr,
        )
        sys.exit(2)

    return Config(
        tenant=tenant,
        bind_host=os.environ.get("MCP_HOST", "0.0.0.0"),
        bind_port=int(os.environ.get("MCP_PORT", "8765")),
        auth_key=os.environ.get("MCP_AUTH_KEY") or None,
        vector_db_dsn=os.environ.get("VECTOR_DB_DSN") or None,
        embedder_url=(os.environ.get("EMBEDDER_URL") or "").rstrip("/") or None,
        mongo_url=os.environ.get("MONGO_URL") or None,
        mongo_db=os.environ.get("MONGO_DB", "otto"),
    )
