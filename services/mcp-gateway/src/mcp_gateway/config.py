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
        # tesserix-home (the company marketing/admin app) routes its Otto
        # chats to the "platform" tenant — the MCP for it serves company
        # info + contact-lead capture rather than per-store order data.
        "platform",
    }
)


@dataclasses.dataclass(frozen=True)
class Config:
    tenant: str
    bind_host: str
    bind_port: int
    # Bearer token clients send via X-MCP-Key. None disables auth (dev only).
    auth_key: str | None
    # Mutating tools are disabled by default. Production MCP surfaces are
    # read-only unless explicitly opted in for a separately controlled use.
    allow_mutations: bool
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
    # Local file paths to OpenAPI specs the gateway loads at startup
    # (in addition to whatever URLs it fetches). Used for the
    # bootstrap phase: each tenant's spec is checked into
    # `services/mcp-gateway/openapi/<tenant>/spec.yaml` and baked
    # into the image so the gateway can auto-register tools without
    # the backend yet owning a live `/openapi.json` route. Migrate to
    # `openapi_urls` per-tenant once the backend serves the spec.
    openapi_files: tuple[str, ...]
    # Static headers attached to every backend call the auto-tools
    # make. Format: `Key: Value, Other: Value2`. Used to forward
    # shared-secret headers that gate the backend routes — e.g.
    # mark8ly's storefront API requires `X-Storefront-Key`. Each pod
    # is tenant-scoped, so this is one tenant's set of secrets.
    openapi_backend_headers: dict[str, str]
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
    # tesserix-home (the "company" app, deployed as the `company` service in
    # the `tesserix` namespace). Used by the platform-tenant tools for the
    # public contact-lead endpoint.
    tesserix_home_url: str
    # marketplace-api ADMIN engine — the /internal/v1/tickets/from-conversation
    # route lives on the admin engine (not storefront). create_support_ticket
    # POSTs here.
    mark8ly_marketplace_api_admin_url: str
    # Shared secret for marketplace-api /internal routes (X-Internal-Auth).
    # None => create_support_ticket returns internal_auth_unconfigured.
    marketplace_internal_auth: str | None
    # Shared HMAC key (base64) for HomeChef's BFF auth (apps/api bff_auth.go).
    # The homechef order tools sign requests with it exactly the way the BFF
    # does, so they can act for a verified customer (scoped server-side to that
    # customer). None => those tools fail closed.
    homechef_bff_hmac_key: str | None


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
        allow_mutations=os.environ.get("MCP_ALLOW_MUTATIONS", "false").strip().lower()
        in {"1", "true", "yes", "on"},
        vector_db_dsn=os.environ.get("VECTOR_DB_DSN") or None,
        embedder_url=(os.environ.get("EMBEDDER_URL") or "").rstrip("/") or None,
        mongo_url=os.environ.get("MONGO_URL") or None,
        mongo_db=os.environ.get("MONGO_DB", "otto"),
        openapi_urls=_split_csv(os.environ.get("MCP_OPENAPI_URLS", "")),
        openapi_files=_split_csv(os.environ.get("MCP_OPENAPI_FILES", "")),
        openapi_backend_headers=_split_headers(os.environ.get("MCP_OPENAPI_HEADERS", "")),
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
            # marketplace-api moved to the mark8ly namespace + per-mode
            # services; the storefront engine serves /api/v1/storefront/*
            # on port 8080 (the old mp-orders.marketplace host no longer
            # resolves, and the service exposes 8080 not 80).
            "http://mark8ly-marketplace-api-storefront.mark8ly.svc.cluster.local:8080",
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
        tesserix_home_url=os.environ.get(
            "TESSERIX_HOME_URL",
            "http://company.tesserix.svc.cluster.local",
        ).rstrip("/"),
        mark8ly_marketplace_api_admin_url=os.environ.get(
            "MARK8LY_MARKETPLACE_API_ADMIN_URL",
            "http://mark8ly-marketplace-api-admin.mark8ly.svc.cluster.local:8080",
        ).rstrip("/"),
        marketplace_internal_auth=os.environ.get("MARKETPLACE_INTERNAL_AUTH") or None,
        homechef_bff_hmac_key=os.environ.get("HOMECHEF_BFF_HMAC_KEY") or None,
    )


def _split_csv(raw: str) -> tuple[str, ...]:
    """Parse a CSV env var, dropping empties and whitespace. Returns a
    tuple so the Config remains hashable (it's a frozen dataclass)."""
    return tuple(part.strip() for part in raw.split(",") if part.strip())


def _split_headers(raw: str) -> dict[str, str]:
    """Parse a comma-separated `Key: Value, Other: Value2` env var
    into a header dict. Malformed entries (no colon) are skipped with
    no error — the gateway must keep starting even with a typo'd
    secret value. Keys are case-preserved so a backend that's picky
    about header case still gets what it expects."""
    out: dict[str, str] = {}
    for part in raw.split(","):
        part = part.strip()
        if not part or ":" not in part:
            continue
        key, _, value = part.partition(":")
        key = key.strip()
        value = value.strip()
        if key:
            out[key] = value
    return out
