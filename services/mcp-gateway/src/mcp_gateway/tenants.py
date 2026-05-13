"""Per-tenant tool definitions.

Each tenant's tools call its own product backend over HTTP (in-cluster
service DNS) and return the parsed response to the SLM. Where a real
backend route doesn't exist yet, the tool returns a structured
`not_implemented` response — never fake data — so the SLM tells the
customer "I can't fetch that yet" instead of inventing numbers.
"""
from __future__ import annotations

import logging
from datetime import datetime, timezone
from typing import Any

import httpx

from .config import Config


logger = logging.getLogger(__name__)


def register(mcp, cfg: Config) -> None:
    """Dispatch to the tenant-specific register function."""
    {
        "mark8ly": _register_mark8ly,
        "fanzone": _register_fanzone,
        "homechef": _register_homechef,
        "stockpilot": _register_stockpilot,
        "gameverse": _register_gameverse,
        "horoscope": _register_horoscope,
        "scrapper": _register_scrapper,
    }[cfg.tenant](mcp, cfg)


# ---------------------------------------------------------------------------
# Shared helpers — every tool follows the same call pattern.
# ---------------------------------------------------------------------------

# Backends are in-cluster and on the customer hot path, so the timeout
# is small enough to fail fast (the orchestrator falls back to RAG /
# generic reply) but big enough to absorb a cold-start Knative pod.
_HTTP_TIMEOUT_SECONDS = 4.0


async def _get_json(
    base_url: str,
    path: str,
    *,
    source: str,
    params: dict[str, Any] | None = None,
    headers: dict[str, str] | None = None,
) -> dict[str, Any]:
    """GET {base_url}{path}; return either the parsed JSON (annotated
    with `source`) or a structured error. Never raises — the caller
    hands the dict back to the SLM as the tool result either way.
    """
    url = f"{base_url}{path}"
    try:
        async with httpx.AsyncClient(timeout=_HTTP_TIMEOUT_SECONDS) as client:
            res = await client.get(url, params=params, headers=headers)
        if 200 <= res.status_code < 300:
            try:
                body = res.json()
            except ValueError:
                body = {"raw": res.text[:500]}
            return {**(body if isinstance(body, dict) else {"data": body}), "source": source}
        return {
            "error": "lookup_failed",
            "status": res.status_code,
            "detail": res.text[:300] if res.text else None,
            "source": source,
        }
    except httpx.HTTPError as exc:
        logger.warning("backend GET %s failed: %s", url, exc)
        return {"error": "backend_unreachable", "detail": str(exc), "source": source}


def _not_implemented(tool: str, reason: str) -> dict[str, Any]:
    """Return value for tools whose product backend doesn't yet expose
    the data over HTTP. The SLM should treat this as a hard signal to
    say 'that feature isn't available right now' — NOT to fabricate.
    """
    return {
        "error": "not_implemented",
        "tool": tool,
        "reason": reason,
        "_action_for_assistant": (
            "Tell the customer this specific lookup isn't available yet, "
            "offer to connect them to a human if relevant. Do NOT invent values."
        ),
    }


def _now() -> datetime:
    return datetime.now(tz=timezone.utc)


def _iso(value: datetime) -> str:
    return value.isoformat()


# ---------------------------------------------------------------------------
# mark8ly — marketplace e-commerce
# ---------------------------------------------------------------------------
def _register_mark8ly(mcp, cfg: Config) -> None:
    @mcp.tool(
        name="get_order",
        description=(
            "Look up a customer order by order_id. Returns status, items, "
            "totals, shipment tracking. Pass store_slug from the conversation "
            "context if known; defaults to 'tesserix-store' otherwise."
        ),
    )
    async def get_order(order_id: str, store_slug: str = "tesserix-store") -> dict[str, Any]:
        path = f"/api/v1/storefront/stores/{store_slug}/orders/{order_id}"
        return await _get_json(cfg.mark8ly_orders_url, path, source="mp-orders")

    @mcp.tool(
        name="list_returns",
        description=(
            "List return requests scoped to a specific order. Pass the order_id "
            "(no email-scoped endpoint exists). Returns RMA id, status, refund amount."
        ),
    )
    async def list_returns(order_id: str, store_slug: str = "tesserix-store", limit: int = 5) -> dict[str, Any]:
        path = f"/api/v1/storefront/stores/{store_slug}/orders/{order_id}/returns"
        return await _get_json(
            cfg.mark8ly_orders_url, path,
            source="mp-orders",
            params={"limit": max(1, min(limit, 25))},
        )

    @mcp.tool(
        name="check_payment_status",
        description=(
            "Resolve a payment by gateway reference (razorpay/stripe). NOT YET "
            "WIRED — the mp-payment service is still stubbed in mark8ly."
        ),
    )
    async def check_payment_status(gateway_reference: str) -> dict[str, Any]:
        return _not_implemented(
            "check_payment_status",
            "mark8ly mp-payment service has no public lookup endpoint yet.",
        ) | {"gateway_reference": gateway_reference}


# ---------------------------------------------------------------------------
# fanzone — cricket fan platform
# ---------------------------------------------------------------------------
def _register_fanzone(mcp, cfg: Config) -> None:
    @mcp.tool(
        name="get_user_points",
        description=(
            "Return the user's CURRENT points balance from the fanzone-user "
            "service. Pass the conversation customer's user_id exactly — "
            "never invent one."
        ),
    )
    async def get_user_points(user_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.fanzone_user_url,
            f"/api/v1/users/{user_id}/points",
            source="fanzone-user",
        )

    @mcp.tool(
        name="get_match_info",
        description="Look up an IPL/T20/ODI match by id from sports-data.",
    )
    async def get_match_info(match_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.fanzone_match_url,
            f"/api/v1/cricket/matches/{match_id}",
            source="sports-data",
        )

    @mcp.tool(
        name="list_user_predictions",
        description="Recent prediction picks for a user: locked, settled, stake.",
    )
    async def list_user_predictions(user_id: str, limit: int = 5) -> dict[str, Any]:
        return await _get_json(
            cfg.fanzone_prediction_url,
            f"/api/v1/predictions/users/{user_id}",
            source="fanzone-prediction",
            params={"limit": max(1, min(limit, 25))},
        )


# ---------------------------------------------------------------------------
# homechef — food delivery
# ---------------------------------------------------------------------------
def _register_homechef(mcp, cfg: Config) -> None:
    @mcp.tool(
        name="get_order_status",
        description="Look up a HomeChef order by id. Status, ETA, chef, items, driver location.",
    )
    async def get_order_status(order_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.homechef_api_url,
            f"/api/v1/orders/{order_id}",
            source="homechef-api",
        )

    @mcp.tool(
        name="get_chef_availability",
        description="Is a chef currently taking orders + their next delivery window.",
    )
    async def get_chef_availability(chef_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.homechef_api_url,
            f"/api/v1/chefs/{chef_id}",
            source="homechef-api",
        )

    @mcp.tool(
        name="track_delivery",
        description="Live delivery state for an in-flight order. Coords + ETA + masked driver phone.",
    )
    async def track_delivery(order_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.homechef_api_url,
            f"/api/v1/orders/{order_id}/track",
            source="homechef-api",
        )


# ---------------------------------------------------------------------------
# stockpilot — AI stock analysis
# ---------------------------------------------------------------------------
def _register_stockpilot(mcp, cfg: Config) -> None:
    _DISCLAIMER = "Not financial advice. Verify before trading."

    @mcp.tool(
        name="get_portfolio_summary",
        description=(
            "Snapshot of the user's portfolio: equity, buying power, top "
            "holdings, daily P/L. Pass the user's stockpilot account_id."
        ),
    )
    async def get_portfolio_summary(account_id: str) -> dict[str, Any]:
        result = await _get_json(
            cfg.stockpilot_api_url,
            f"/api/portfolios/{account_id}",
            source="stockpilot-api",
        )
        result.setdefault("_disclaimer", _DISCLAIMER)
        return result

    @mcp.tool(
        name="get_broker_status",
        description="Alpaca broker connection health for the user's account.",
    )
    async def get_broker_status(account_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.stockpilot_api_url,
            f"/api/broker/alpaca/accounts/{account_id}",
            source="stockpilot-api",
        )

    @mcp.tool(
        name="get_agent_trace",
        description=(
            "LangGraph agent trace for a recent run on a symbol. NOT YET WIRED "
            "— stockpilot exposes reports via /api/agents/reports/{report_id} "
            "but not a generic trace-by-id endpoint."
        ),
    )
    async def get_agent_trace(symbol: str, run_id: str | None = None) -> dict[str, Any]:
        return _not_implemented(
            "get_agent_trace",
            "stockpilot agent traces aren't exposed by trace_id; reports are by report_id only.",
        ) | {"symbol": symbol, "run_id": run_id}


# ---------------------------------------------------------------------------
# gameverse — multiplayer board games
# ---------------------------------------------------------------------------
def _register_gameverse(mcp, cfg: Config) -> None:
    # gameverse-server is currently WebSocket-only — there's no REST
    # surface for room state, user ratings, or match history. Return
    # structured not_implemented so the SLM doesn't fabricate scores.
    @mcp.tool(
        name="get_room_state",
        description="Snapshot of a game room. NOT YET WIRED — gameverse-server is WebSocket-only.",
    )
    async def get_room_state(room_code: str) -> dict[str, Any]:
        return _not_implemented(
            "get_room_state",
            "gameverse-server streams room state via WebSocket; no GET endpoint yet.",
        ) | {"room_code": room_code}

    @mcp.tool(
        name="get_user_rating",
        description="Glicko-2 rating per game. NOT YET WIRED — no public ratings endpoint.",
    )
    async def get_user_rating(user_id: str) -> dict[str, Any]:
        return _not_implemented(
            "get_user_rating",
            "gameverse hasn't exposed Glicko-2 ratings over HTTP yet.",
        ) | {"user_id": user_id}

    @mcp.tool(
        name="get_match_history",
        description="Recent matches for a user. NOT YET WIRED — no match-history endpoint.",
    )
    async def get_match_history(user_id: str, limit: int = 5) -> dict[str, Any]:
        return _not_implemented(
            "get_match_history",
            "gameverse-server stores matches in PostgreSQL but doesn't expose them over HTTP.",
        ) | {"user_id": user_id}


# ---------------------------------------------------------------------------
# horoscope — astrology
# ---------------------------------------------------------------------------
def _register_horoscope(mcp, cfg: Config) -> None:
    _DISCLAIMER = "For entertainment / self-reflection only."

    @mcp.tool(
        name="get_chart_summary",
        description=(
            "Sun/Moon/Ascendant + dominant element/modality for the customer. "
            "Pass tradition='western' or 'vedic'. Backend reads identity from "
            "X-User-Id so we forward the customer's user_id as that header."
        ),
    )
    async def get_chart_summary(user_id: str, tradition: str = "western") -> dict[str, Any]:
        result = await _get_json(
            cfg.horoscope_api_url,
            "/api/v1/me/chart",
            source="horoscope-api",
            params={"tradition": tradition},
            headers={"X-User-Id": user_id} if user_id else None,
        )
        result.setdefault("_disclaimer", _DISCLAIMER)
        return result

    @mcp.tool(
        name="get_today_transit",
        description="Notable transits affecting this customer today.",
    )
    async def get_today_transit(user_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.horoscope_api_url,
            "/api/v1/me/reading",
            source="horoscope-api",
            headers={"X-User-Id": user_id} if user_id else None,
        )

    @mcp.tool(
        name="list_recent_readings",
        description="Recent visual readings (palm/face/chart) on file for the user.",
    )
    async def list_recent_readings(user_id: str, limit: int = 3) -> dict[str, Any]:
        return await _get_json(
            cfg.horoscope_api_url,
            "/api/v1/me/readings/visual",
            source="horoscope-api",
            params={"limit": max(1, min(limit, 25))},
            headers={"X-User-Id": user_id} if user_id else None,
        )


# ---------------------------------------------------------------------------
# scrapper — social media intel
# ---------------------------------------------------------------------------
def _register_scrapper(mcp, cfg: Config) -> None:
    # The scrapper FastAPI backend (src/api/server.py) exposes
    # /api/jobs/{id}, /api/campaigns, /api/accounts — wire them.
    @mcp.tool(
        name="get_scrape_job",
        description="State of a scrape job: progress, profile count, per-platform errors.",
    )
    async def get_scrape_job(job_id: str) -> dict[str, Any]:
        return await _get_json(
            cfg.scrapper_api_url,
            f"/api/jobs/{job_id}",
            source="scrapper-api",
        )

    @mcp.tool(
        name="list_publishing_pipelines",
        description="Active publishing campaigns + target platforms.",
    )
    async def list_publishing_pipelines(limit: int = 10) -> dict[str, Any]:
        return await _get_json(
            cfg.scrapper_api_url,
            "/api/campaigns",
            source="scrapper-api",
            params={"limit": max(1, min(limit, 50))},
        )

    @mcp.tool(
        name="list_connected_accounts",
        description="Connected social platform accounts: handle, token state, expiry.",
    )
    async def list_connected_accounts() -> dict[str, Any]:
        return await _get_json(
            cfg.scrapper_api_url,
            "/api/accounts",
            source="scrapper-api",
        )


__all__ = ["register"]
