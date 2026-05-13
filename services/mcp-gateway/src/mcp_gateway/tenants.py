"""Per-tenant tool definitions.

Every tool returns a STUB response that mirrors the shape a real
backend would return. The stub flag (`"_stub": true`) is included so
the AI agent can caveat its reply with "based on a sample response …".
Product teams replace the stub bodies with real httpx calls to their
own APIs as the integrations land — the tool signature and return
shape stay the same so the SLM doesn't need re-training.
"""
from __future__ import annotations

from datetime import datetime, timedelta, timezone
from typing import Any

from .config import Config


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
    }[cfg.tenant](mcp)


# ---------------------------------------------------------------------------
# mark8ly — marketplace e-commerce
# ---------------------------------------------------------------------------
def _register_mark8ly(mcp) -> None:
    @mcp.tool(
        name="get_order",
        description=(
            "Look up a customer order by order id. Returns status, items, "
            "totals, shipment tracking and the merchant who owns the order."
        ),
    )
    async def get_order(order_id: str) -> dict[str, Any]:
        return {
            "order_id": order_id,
            "status": "shipped",
            "merchant": {"slug": "tesserix-store", "name": "Tesserix Store"},
            "placed_at": _iso(_now() - timedelta(days=2)),
            "shipped_at": _iso(_now() - timedelta(hours=18)),
            "estimated_delivery": _iso(_now() + timedelta(days=1)),
            "items": [
                {"sku": "SKU-001", "name": "Sample Product", "qty": 1, "price_cents": 4999},
            ],
            "totals": {"subtotal_cents": 4999, "shipping_cents": 0, "total_cents": 4999, "currency": "INR"},
            "tracking": {"carrier": "BlueDart", "tracking_number": "BD-2026-XXXX"},
            "_stub": True,
        }

    @mcp.tool(
        name="list_returns",
        description="List return requests for a customer email. Status, RMA id, refund amount.",
    )
    async def list_returns(email: str, limit: int = 5) -> dict[str, Any]:
        return {
            "email": email,
            "returns": [
                {
                    "rma_id": "RMA-2025-0001",
                    "order_id": "ORD-2025-12345",
                    "status": "refund_issued",
                    "amount_cents": 2499,
                    "currency": "INR",
                    "created_at": _iso(_now() - timedelta(days=4)),
                }
            ][:limit],
            "_stub": True,
        }

    @mcp.tool(
        name="check_payment_status",
        description="Resolve a payment by gateway reference. Useful when a customer paid but the order shows unpaid.",
    )
    async def check_payment_status(gateway_reference: str) -> dict[str, Any]:
        return {
            "gateway_reference": gateway_reference,
            "gateway": "razorpay",
            "state": "captured",
            "amount_cents": 4999,
            "currency": "INR",
            "captured_at": _iso(_now() - timedelta(minutes=12)),
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# fanzone — cricket fan platform
# ---------------------------------------------------------------------------
def _register_fanzone(mcp) -> None:
    @mcp.tool(
        name="get_user_points",
        description="Return the user's current points balance, weekly delta, and leaderboard rank.",
    )
    async def get_user_points(user_id: str) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "points": 1240,
            "delta_7d": 180,
            "leaderboard_rank": 4_217,
            "tier": "silver",
            "_stub": True,
        }

    @mcp.tool(
        name="get_match_info",
        description="Look up an IPL/T20/ODI match by id. Returns status, score, toss, and play state.",
    )
    async def get_match_info(match_id: str) -> dict[str, Any]:
        return {
            "match_id": match_id,
            "tournament": "IPL 2026",
            "teams": {"home": "MI", "away": "CSK"},
            "status": "live",
            "score": {"MI": {"runs": 142, "wickets": 4, "overs": 16.2}, "CSK": None},
            "toss": {"winner": "MI", "decision": "bat"},
            "venue": "Wankhede Stadium",
            "_stub": True,
        }

    @mcp.tool(
        name="list_user_predictions",
        description="Return the user's recent prediction picks, locked status, and settled outcome.",
    )
    async def list_user_predictions(user_id: str, limit: int = 5) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "predictions": [
                {
                    "match_id": "IPL-2026-042",
                    "question": "Match winner",
                    "pick": "MI",
                    "locked": True,
                    "settled": False,
                    "stake_points": 50,
                }
            ][:limit],
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# homechef — food delivery
# ---------------------------------------------------------------------------
def _register_homechef(mcp) -> None:
    @mcp.tool(
        name="get_order_status",
        description="Look up a HomeChef order by id. Returns status, ETA, chef, items and live driver location if available.",
    )
    async def get_order_status(order_id: str) -> dict[str, Any]:
        return {
            "order_id": order_id,
            "status": "out_for_delivery",
            "chef": {"id": "chef-rachna", "name": "Rachna Mehta"},
            "items": [{"name": "Veg thali", "qty": 1}],
            "eta_minutes": 12,
            "driver": {"name": "Anil", "phone_masked": "+91-XXXXX-XX42", "lat": 12.9716, "lng": 77.5946},
            "_stub": True,
        }

    @mcp.tool(
        name="get_chef_availability",
        description="Check whether a chef is currently open for orders and their next available delivery window.",
    )
    async def get_chef_availability(chef_id: str) -> dict[str, Any]:
        return {
            "chef_id": chef_id,
            "open_now": True,
            "next_window_open_at": _iso(_now() + timedelta(hours=4)),
            "lead_time_minutes": 45,
            "_stub": True,
        }

    @mcp.tool(
        name="track_delivery",
        description="Live delivery state for an in-flight order. Returns coordinates + ETA + driver phone (masked).",
    )
    async def track_delivery(order_id: str) -> dict[str, Any]:
        return {
            "order_id": order_id,
            "state": "en_route",
            "driver_lat": 12.9716,
            "driver_lng": 77.5946,
            "distance_km": 1.4,
            "eta_minutes": 9,
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# stockpilot — AI stock analysis
# ---------------------------------------------------------------------------
def _register_stockpilot(mcp) -> None:
    @mcp.tool(
        name="get_portfolio_summary",
        description="Snapshot of the user's portfolio: equity, buying power, top holdings, daily P/L.",
    )
    async def get_portfolio_summary(account_id: str) -> dict[str, Any]:
        return {
            "account_id": account_id,
            "equity_cents": 12_345_67,
            "cash_cents": 1_500_00,
            "buying_power_cents": 3_000_00,
            "daily_pl_cents": -45_22,
            "top_holdings": [
                {"symbol": "NVDA", "qty": 12, "market_value_cents": 240_00_00, "weight": 0.42},
                {"symbol": "AAPL", "qty": 20, "market_value_cents": 180_00_00, "weight": 0.31},
            ],
            "_stub": True,
            "_disclaimer": "Not financial advice. Verify before trading.",
        }

    @mcp.tool(
        name="get_broker_status",
        description="Health of the Alpaca broker connection: OAuth state, last sync time, error details if any.",
    )
    async def get_broker_status(account_id: str) -> dict[str, Any]:
        return {
            "account_id": account_id,
            "broker": "alpaca",
            "mode": "paper",
            "connected": True,
            "last_sync_at": _iso(_now() - timedelta(minutes=2)),
            "error": None,
            "_stub": True,
        }

    @mcp.tool(
        name="get_agent_trace",
        description="Return the LangGraph agent's trace for the most recent run on a symbol. Steps, tool calls, final decision.",
    )
    async def get_agent_trace(symbol: str, run_id: str | None = None) -> dict[str, Any]:
        return {
            "symbol": symbol,
            "run_id": run_id or "agent-run-stub-0001",
            "steps": [
                {"agent": "supervisor", "action": "fan_out", "at": _iso(_now() - timedelta(minutes=4))},
                {"agent": "fundamental_analyst", "action": "score", "result": "Hold", "at": _iso(_now() - timedelta(minutes=3))},
                {"agent": "technical_analyst", "action": "score", "result": "Buy", "at": _iso(_now() - timedelta(minutes=2))},
                {"agent": "supervisor", "action": "synthesise", "result": "Hold", "at": _iso(_now() - timedelta(minutes=1))},
            ],
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# gameverse — multiplayer board games
# ---------------------------------------------------------------------------
def _register_gameverse(mcp) -> None:
    @mcp.tool(
        name="get_room_state",
        description="Snapshot of a game room: players, turn order, last move, current game state.",
    )
    async def get_room_state(room_code: str) -> dict[str, Any]:
        return {
            "room_code": room_code,
            "game": "ludo",
            "status": "in_progress",
            "turn": "player_2",
            "players": [
                {"slot": 1, "user_id": "user1@ludo.com", "color": "red", "tokens_home": 2},
                {"slot": 2, "user_id": "user2@ludo.com", "color": "green", "tokens_home": 1},
                {"slot": 3, "user_id": "user3@ludo.com", "color": "yellow", "tokens_home": 0},
                {"slot": 4, "user_id": "user4@ludo.com", "color": "blue", "tokens_home": 0},
            ],
            "last_move": {"player": "player_1", "die_roll": 5, "token_moved": 2},
            "_stub": True,
        }

    @mcp.tool(
        name="get_user_rating",
        description="User's Glicko-2 rating per game plus win/loss/draw counts.",
    )
    async def get_user_rating(user_id: str) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "ratings": {
                "ludo": {"rating": 1620, "rd": 90, "wins": 14, "losses": 9, "draws": 1},
                "chess": {"rating": 1505, "rd": 110, "wins": 4, "losses": 6, "draws": 0},
            },
            "_stub": True,
        }

    @mcp.tool(
        name="get_match_history",
        description="Recent matches for a user. Result, opponent, rating change.",
    )
    async def get_match_history(user_id: str, limit: int = 5) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "matches": [
                {
                    "game": "ludo",
                    "opponent_user_ids": ["user2@ludo.com"],
                    "result": "win",
                    "rating_delta": 14,
                    "played_at": _iso(_now() - timedelta(hours=2)),
                }
            ][:limit],
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# horoscope — astrology
# ---------------------------------------------------------------------------
def _register_horoscope(mcp) -> None:
    @mcp.tool(
        name="get_chart_summary",
        description="Sun/moon/ascendant + dominant element/modality. Tradition: 'western' or 'vedic'.",
    )
    async def get_chart_summary(user_id: str, tradition: str = "western") -> dict[str, Any]:
        return {
            "user_id": user_id,
            "tradition": tradition,
            "sun": {"sign": "Cancer", "degrees": 14.2},
            "moon": {"sign": "Pisces", "degrees": 22.8},
            "ascendant": {"sign": "Libra", "degrees": 3.1},
            "dominant": {"element": "water", "modality": "cardinal"},
            "_stub": True,
            "_disclaimer": "For entertainment / self-reflection only.",
        }

    @mcp.tool(
        name="get_today_transit",
        description="Notable transits affecting this user today.",
    )
    async def get_today_transit(user_id: str) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "transits": [
                {"planet": "Moon", "aspect": "trine", "natal_planet": "Venus", "exact_at": _iso(_now() + timedelta(hours=6))}
            ],
            "_stub": True,
        }

    @mcp.tool(
        name="list_recent_readings",
        description="Recent readings (palm / face / chart-based composed text) the user has on file.",
    )
    async def list_recent_readings(user_id: str, limit: int = 3) -> dict[str, Any]:
        return {
            "user_id": user_id,
            "readings": [
                {
                    "kind": "chart",
                    "summary": "Cancer Sun seeking a more grounded routine in the weeks ahead…",
                    "created_at": _iso(_now() - timedelta(days=1)),
                }
            ][:limit],
            "_stub": True,
        }


# ---------------------------------------------------------------------------
# scrapper — social media intel
# ---------------------------------------------------------------------------
def _register_scrapper(mcp) -> None:
    @mcp.tool(
        name="get_scrape_job",
        description="State of a single scrape job: progress, profile count, errors per platform.",
    )
    async def get_scrape_job(job_id: str) -> dict[str, Any]:
        return {
            "job_id": job_id,
            "status": "running",
            "progress_pct": 67,
            "profiles_scraped": 1_240,
            "errors_by_platform": {"instagram": 0, "x": 2, "facebook": 0, "reddit": 0, "linkedin": 1, "tiktok": 0},
            "_stub": True,
        }

    @mcp.tool(
        name="list_publishing_pipelines",
        description="Active publishing pipelines and the platforms they target.",
    )
    async def list_publishing_pipelines() -> dict[str, Any]:
        return {
            "pipelines": [
                {"id": "pl-001", "name": "Daily campaign", "platforms": ["x", "facebook", "linkedin"], "active": True},
            ],
            "_stub": True,
        }

    @mcp.tool(
        name="list_connected_accounts",
        description="Connected platform accounts for the operator: token state + expiry.",
    )
    async def list_connected_accounts() -> dict[str, Any]:
        return {
            "accounts": [
                {"platform": "x", "handle": "@example", "token_state": "valid", "expires_at": _iso(_now() + timedelta(days=30))},
                {"platform": "linkedin", "handle": "example-co", "token_state": "expiring_soon", "expires_at": _iso(_now() + timedelta(days=2))},
            ],
            "_stub": True,
        }


# ---------------------------------------------------------------------------
def _now() -> datetime:
    return datetime.now(tz=timezone.utc)


def _iso(value: datetime) -> str:
    return value.isoformat()


__all__ = ["register"]
