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
    # OpenAPI spec URLs the gateway pulls at startup. Every operation
    # in those specs tagged `x-mcp-expose: customer-read` is auto-
    # registered as an MCP tool (see openapi_loader + auto_tools). This
    # is the path to broad customer-data coverage without hand-coding a
    # tool per endpoint. CSV via `MCP_OPENAPI_URLS`. Empty list = the
    # gateway only serves hand-rolled tools (current default while
    # backends are still being annotated).
    openapi_urls: tuple[str, ...]
    # Per-tenant backend URLs. The MCP tools use these to call into
    # the product's own services (e.g. fanzone-user for points,
    # mark8ly orders for order lookups). Defaults match the in-cluster
    # service DNS so a typical deploy doesn't need to set anything.
    fanzone_user_url: str
    fanzone_match_url: str
    fanzone_prediction_url: str
    mark8ly_orders_url: str
    homechef_api_url: str
    stockpilot_api_url: str
    gameverse_server_url: str
    horoscope_api_url: str
    scrapper_api_url: str


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
        openapi_urls=_split_csv(os.environ.get("MCP_OPENAPI_URLS", "")),
        fanzone_user_url=os.environ.get(
            "FANZONE_USER_URL",
            "http://fanzone-user.fanzone.svc.cluster.local",
        ).rstrip("/"),
        fanzone_match_url=os.environ.get(
            "FANZONE_MATCH_URL",
            "http://sports-data.fanzone.svc.cluster.local",
        ).rstrip("/"),
        fanzone_prediction_url=os.environ.get(
            "FANZONE_PREDICTION_URL",
            "http://fanzone-prediction.fanzone.svc.cluster.local",
        ).rstrip("/"),
        mark8ly_orders_url=os.environ.get(
            "MARK8LY_ORDERS_URL",
            "http://mp-orders.marketplace.svc.cluster.local",
        ).rstrip("/"),
        homechef_api_url=os.environ.get(
            "HOMECHEF_API_URL",
            "http://homechef-api.homechef.svc.cluster.local",
        ).rstrip("/"),
        stockpilot_api_url=os.environ.get(
            "STOCKPILOT_API_URL",
            "http://stockpilot-api.stockpilot.svc.cluster.local",
        ).rstrip("/"),
        gameverse_server_url=os.environ.get(
            "GAMEVERSE_SERVER_URL",
            "http://gameverse-server.gameverse.svc.cluster.local",
        ).rstrip("/"),
        horoscope_api_url=os.environ.get(
            "HOROSCOPE_API_URL",
            "http://horoscope-api.horoscope.svc.cluster.local",
        ).rstrip("/"),
        scrapper_api_url=os.environ.get(
            "SCRAPPER_API_URL",
            "http://scrapper-api.scrapper.svc.cluster.local",
        ).rstrip("/"),
    )


def _split_csv(raw: str) -> tuple[str, ...]:
    """Parse a CSV env var, dropping empties and whitespace. Returns a
    tuple so the Config remains hashable (it's a frozen dataclass)."""
    return tuple(part.strip() for part in raw.split(",") if part.strip())
