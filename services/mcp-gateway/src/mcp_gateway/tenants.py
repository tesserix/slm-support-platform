"""Per-tenant tool definitions.

Each tenant's tools call its own product backend over HTTP (in-cluster
service DNS) and return the parsed response to the SLM. Where a real
backend route doesn't exist yet, the tool returns a structured
`not_implemented` response — never fake data — so the SLM tells the
customer "I can't fetch that yet" instead of inventing numbers.
"""

from __future__ import annotations

import base64
import hashlib
import hmac
import logging
import time
from datetime import datetime, timezone
from typing import Any
from urllib.parse import urlencode

import httpx

from .config import Config

logger = logging.getLogger(__name__)


def _trusted_ctx() -> dict[str, str]:
    """Trusted conversation/customer context for the current MCP request,
    forwarded by slm-router as validated HTTP headers (see server.py
    request_ctx). Tools use this — NOT model-supplied args — for the
    conversation/tenant/store/customer identity, so ticket + refund
    requests are attributable and can't be spoofed via tool arguments.
    Lazy import of server avoids the server->tenants import cycle."""
    try:
        from .server import request_ctx

        return request_ctx.get() or {}
    except Exception:
        return {}


def _customer_scoped_headers(cfg: Config) -> dict[str, str]:
    """Storefront backend headers PLUS the verified customer's email, so the
    backend scopes the order read/write to THIS customer. The email is taken
    from the trusted conversation context (OTP/session-verified), never from a
    model argument — so the assistant cannot be used to read or act on another
    customer's order by passing someone else's order id."""
    headers = dict(cfg.openapi_backend_headers or {})
    email = _trusted_ctx().get("customer_email", "")
    if email:
        headers["X-Customer-Email"] = email
    return headers


def _homechef_signed_headers(
    cfg: Config, method: str, path: str, body: bytes = b""
) -> dict[str, str] | None:
    """Sign a homechef-api request exactly the way HomeChef's BFF does
    (apps/api/middleware/bff_auth.go `compute`): HMAC-SHA256 over
    "<method>\\n<path>\\n<hex(sha256(body))>\\n<ts>" with the shared BFF HMAC
    key (base64), plus the verified customer identity headers. The BFF signs
    r.URL.Path only — NOT the query string — so `path` must carry no query.

    Returns None (fail closed) when the key or the verified customer id is
    missing, so the assistant can never make an unauthenticated call or act for
    an unverified user. The backend scopes every order to X-User-Id, so the
    assistant only ever sees this customer's orders."""
    if not cfg.homechef_bff_hmac_key:
        return None
    ctx = _trusted_ctx()
    user_id = ctx.get("customer_id", "")
    if not user_id:
        return None
    try:
        key = base64.b64decode(cfg.homechef_bff_hmac_key)
    except Exception:
        return None
    ts = str(int(time.time()))
    body_hash = hashlib.sha256(body).hexdigest()
    msg = f"{method}\n{path}\n{body_hash}\n{ts}".encode()
    sig = hmac.new(key, msg, hashlib.sha256).hexdigest()
    headers = {
        "X-Internal-Auth": sig,
        "X-Auth-Ts": ts,
        "X-User-Id": user_id,
    }
    email = ctx.get("customer_email", "")
    if email:
        headers["X-User-Email"] = email
    return headers


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
        "platform": _register_platform,
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


async def _post(
    base_url: str,
    path: str,
    *,
    source: str,
    json_body: dict[str, Any] | None = None,
    form: dict[str, Any] | None = None,
    content: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> dict[str, Any]:
    """POST {base_url}{path} with a JSON or form body; return the parsed
    JSON (annotated with `source`) or a structured error. Never raises.

    Used by the *request*-creating tools (e.g. create_refund_request).
    These tools NEVER mutate money — they file a request (a return in
    'requested' state / an order issue) that a human owner or admin must
    approve before any refund is issued.
    """
    url = f"{base_url}{path}"
    try:
        async with httpx.AsyncClient(timeout=_HTTP_TIMEOUT_SECONDS) as client:
            res = await client.post(
                url, json=json_body, data=form, content=content, headers=headers
            )
        if 200 <= res.status_code < 300:
            try:
                body = res.json()
            except ValueError:
                body = {"raw": res.text[:500]}
            return {**(body if isinstance(body, dict) else {"data": body}), "source": source}
        return {
            "error": "request_failed",
            "status": res.status_code,
            "detail": res.text[:300] if res.text else None,
            "source": source,
        }
    except httpx.HTTPError as exc:
        logger.warning("backend POST %s failed: %s", url, exc)
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


def _clean_date(value: Any) -> str:
    """Accept a YYYY-MM-DD date, reject anything else.

    A small model will happily emit "12th July" or "last Tuesday". Passing that
    through would make the backend ignore the filter and silently widen the
    window back to every order — so validate here and ask, rather than answer
    confidently about the wrong dates.
    """
    text = str(value or "").strip()
    if not text:
        return ""
    try:
        return datetime.strptime(text, "%Y-%m-%d").strftime("%Y-%m-%d")
    except (ValueError, TypeError):
        return ""


def _bad_date(field: str, given: Any) -> dict[str, Any]:
    return {
        "error": "bad_date",
        "field": field,
        "given": str(given),
        "_action_for_assistant": (
            f"{field} must be an exact calendar date as YYYY-MM-DD. Work out the "
            "date the customer means (ask them if it is ambiguous) and call this "
            "tool again — do not describe results you have not fetched."
        ),
    }


def _check_range(days: Any, max_days: int) -> dict[str, Any] | int:
    """Validate a customer-specified `days` window for history tools.

    Returns either a clamped int (when within bounds) or a structured
    `range_exceeded` error the SLM will surface back as a ticket
    suggestion. The exact `_action_for_assistant` text mirrors what's
    in the universal response rules so the SLM stays consistent.
    """
    try:
        d = int(days)
    except (TypeError, ValueError):
        d = 7
    if d < 1:
        d = 1
    if d > max_days:
        return {
            "error": "range_exceeded",
            "requested_days": d,
            "max_days": max_days,
            "_action_for_assistant": (
                f"Tell the customer: 'I can only break this down for the last "
                f"{max_days} days here. For anything longer, please raise a "
                f"support ticket and the team will pull the full record.' "
                f"Do NOT fabricate any values for the longer window."
            ),
        }
    return d


def _entry_ts(entry: dict[str, Any]) -> datetime | None:
    """Best-effort ISO8601 parse off whichever timestamp field the
    backend uses. Returns None when nothing parses cleanly."""
    for key in (
        "created_at",
        "createdAt",
        "timestamp",
        "at",
        "placed_at",
        "occurred_at",
        "ts",
        "date",
    ):
        raw = entry.get(key)
        if not raw:
            continue
        try:
            return datetime.fromisoformat(str(raw).replace("Z", "+00:00"))
        except ValueError:
            continue
    return None


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
        # Fail closed: without a verified customer email we can't prove the order
        # belongs to this customer, so refuse rather than risk leaking someone
        # else's order. The backend ALSO scopes by X-Customer-Email (defence in
        # depth) — see _customer_scoped_headers.
        if not _trusted_ctx().get("customer_email"):
            return {
                "error": "identity_unverified",
                "_action_for_assistant": (
                    "Can't securely verify whose order this is. Ask the customer "
                    "to sign in (or verify their email) and try again."
                ),
            }
        path = f"/api/v1/storefront/stores/{store_slug}/orders/{order_id}"
        return await _get_json(
            cfg.mark8ly_orders_url,
            path,
            source="mp-orders",
            headers=_customer_scoped_headers(cfg),
        )

    @mcp.tool(
        name="list_returns",
        description=(
            "List return requests scoped to a specific order. Pass the order_id "
            "(no email-scoped endpoint exists). Returns RMA id, status, refund amount."
        ),
    )
    async def list_returns(
        order_id: str, store_slug: str = "tesserix-store", limit: int = 5
    ) -> dict[str, Any]:
        path = f"/api/v1/storefront/stores/{store_slug}/orders/{order_id}/returns"
        return await _get_json(
            cfg.mark8ly_orders_url,
            path,
            source="mp-orders",
            params={"limit": max(1, min(limit, 25))},
            headers=_customer_scoped_headers(cfg),
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

    @mcp.tool(
        name="list_recent_orders",
        description=(
            "Find the customer's orders. mark8ly identifies orders by ORDER "
            "NUMBER — there is no email-based list, and the content guard blocks "
            "emails in chat anyway. This tool returns a hint to collect the order "
            "number; do the actual lookup with get_order."
        ),
    )
    async def list_recent_orders(
        email: str = "", days: int = 30, store_slug: str = "tesserix-store"
    ) -> dict[str, Any]:
        # There is no by-email order-list endpoint, and the content guard blocks
        # customers from sharing email/phone in chat — so order lookups are by
        # order number via get_order. Return a clear signal instead of calling a
        # missing endpoint (which 404s). A future verified-context list endpoint
        # could restore an email-free "all my orders" view.
        return {
            "error": "use_order_number",
            "source": "mp-orders",
            "_action_for_assistant": (
                "There's no email-based order list. Ask the customer for their "
                "order number (it's on their order-confirmation email and the "
                "Orders page of their account) and call get_order. Never ask for "
                "or accept their email or phone number in chat."
            ),
        }

    @mcp.tool(
        name="create_refund_request",
        description=(
            "File a return/refund REQUEST for an order. This does NOT issue a "
            "refund — it creates a return in 'requested' state that the store "
            "owner/admin must review and approve before any money moves. Use it "
            "only after you've looked up the order (get_order) and confirmed the "
            "items. Put your investigation summary (eligibility, what the "
            "customer reported, recommendation) in `findings`; it is saved on the "
            "request for the approver. items is a list of "
            "{order_item_id, quantity} for the lines to return."
        ),
    )
    async def create_refund_request(
        order_id: str,
        items: list[dict[str, Any]],
        reason: str,
        findings: str = "",
        store_slug: str = "tesserix-store",
        type: str = "return",
        currency_code: str = "INR",
    ) -> dict[str, Any]:
        if not items:
            return {
                "error": "items_required",
                "_action_for_assistant": (
                    "Call get_order first, then pass the order_item_id + quantity "
                    "for each line the customer wants to return. Never guess ids."
                ),
            }
        # Stamp the trusted conversation id (traceability): the request is
        # correlated to the Otto conversation it came from. Taken from the
        # verified request context, never from the model.
        ctx = _trusted_ctx()
        conv = ctx.get("conversation_id", "")
        notes = findings
        if conv:
            notes = (findings + f"\n\n[otto_conversation: {conv}]").strip()
        body = {
            "type": type if type in ("return", "replace") else "return",
            "reason": reason,
            "notes": notes,
            "items": items,
            "currency_code": currency_code,
            "conversation_id": conv,  # ignored by the handler if unknown; aids traceability
        }
        result = await _post(
            cfg.mark8ly_orders_url,
            f"/api/v1/storefront/stores/{store_slug}/orders/{order_id}/returns",
            source="mp-orders",
            json_body=body,
            headers=_customer_scoped_headers(cfg),
        )
        # Make the human-approval gate explicit to the assistant + customer.
        if "error" not in result:
            result["_status"] = "pending_owner_approval"
            result["conversation_id"] = conv
            result["_action_for_assistant"] = (
                "Tell the customer their refund/return REQUEST has been filed and "
                "is awaiting the store owner's approval — no refund is issued until "
                "they approve. Share the return id/number if present."
            )
        return result

    @mcp.tool(
        name="create_support_ticket",
        description=(
            "Open a TRACKED support ticket for THIS conversation when the issue "
            "needs human follow-up or a durable record (you can't fully resolve it "
            "in chat, or it should leave a paper trail). This does NOT resolve the "
            "issue. The conversation, customer and store are taken from the verified "
            "conversation context automatically — you only supply a short subject "
            "and a summary of the issue plus your findings. Returns the ticket "
            "reference; calling it again for the same conversation returns the same "
            "ticket (idempotent)."
        ),
    )
    async def create_support_ticket(
        subject: str,
        summary: str,
        escalation_reason: str = "",
    ) -> dict[str, Any]:
        ctx = _trusted_ctx()
        missing = [
            k
            for k in ("conversation_id", "tenant_id", "store_id", "customer_email")
            if not ctx.get(k)
        ]
        if missing:
            return {
                "error": "missing_context",
                "missing": missing,
                "_action_for_assistant": (
                    "Couldn't open a ticket — required context is missing"
                    + (
                        " (need the customer's email — ask for it)"
                        if missing == ["customer_email"]
                        else ""
                    )
                    + ". Apologise and offer to connect them to a human instead."
                ),
            }
        if not cfg.marketplace_internal_auth:
            return _not_implemented(
                "create_support_ticket",
                "ticket backend auth (MARKETPLACE_INTERNAL_AUTH) is not configured for this gateway.",
            )
        body = {
            "conversation_id": ctx["conversation_id"],
            "tenant_id": ctx["tenant_id"],
            "store_id": ctx["store_id"],
            "customer_email": ctx["customer_email"],
            "customer_name": ctx.get("customer_name", ""),
            "subject": subject,
            "description": summary,
            "escalation_reason": escalation_reason,
        }
        result = await _post(
            cfg.mark8ly_marketplace_api_admin_url,
            "/internal/v1/tickets/from-conversation",
            source="mp-tickets",
            json_body=body,
            headers={"X-Internal-Auth": cfg.marketplace_internal_auth},
        )
        if "error" not in result:
            result["_status"] = "ticket_created"
            result["conversation_id"] = ctx["conversation_id"]
            result["_action_for_assistant"] = (
                "Tell the customer a support ticket has been opened for their issue "
                "(share the ticket id/number if present) and that the team will "
                "follow up. Do not promise a specific resolution or time."
            )
        return result


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
        name="get_points_history",
        description=(
            "Daily breakdown of a user's points activity over the last "
            "N days. Pass days=7 by default, max 14. If the customer asks "
            "for MORE THAN 14 days the tool returns a 'range_exceeded' "
            "error — surface that to the customer and ask them to open a "
            "support ticket instead of guessing values."
        ),
    )
    async def get_points_history(user_id: str, days: int = 7) -> dict[str, Any]:
        clamped = _check_range(days, max_days=14)
        if isinstance(clamped, dict):
            return clamped | {"source": "fanzone-user"}
        result = await _get_json(
            cfg.fanzone_user_url,
            f"/api/v1/users/{user_id}/points/history",
            source="fanzone-user",
            params={"limit": 100},
        )
        if "error" in result:
            return result
        from datetime import timedelta as _td

        cutoff = _now() - _td(days=clamped)
        days = clamped
        entries = result.get("entries") or []
        buckets: dict[str, dict[str, int]] = {}
        in_window = 0
        for entry in entries:
            ts = _entry_ts(entry)
            if ts is None or ts < cutoff:
                continue
            day = ts.date().isoformat()
            slot = buckets.setdefault(day, {"earned": 0, "spent": 0, "net": 0})
            # Best-effort field shape from the GORM ledger row.
            amt_int = int(entry.get("amount") or entry.get("points") or 0)
            kind = (entry.get("type") or entry.get("kind") or "").lower()
            if amt_int >= 0 and "spen" not in kind:
                slot["earned"] += amt_int
            else:
                slot["spent"] += abs(amt_int)
            slot["net"] += amt_int if "spen" not in kind else -abs(amt_int)
            in_window += 1
        # Oldest first so the SLM can summarise chronologically.
        days_sorted = sorted(buckets.keys())
        return {
            "user_id": user_id,
            "days_requested": days,
            "entries_in_window": in_window,
            "by_day": [{"date": d, **buckets[d]} for d in days_sorted],
            "source": "fanzone-user",
        }

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
        path = f"/api/v1/orders/{order_id}"
        headers = _homechef_signed_headers(cfg, "GET", path)
        if headers is None:
            return {
                "error": "identity_unverified",
                "_action_for_assistant": (
                    "Can't securely verify the customer to look up their order. "
                    "Ask them to sign in and try again."
                ),
            }
        return await _get_json(
            cfg.homechef_api_url,
            path,
            source="homechef-api",
            headers=headers,
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
        path = f"/api/v1/orders/{order_id}/track"
        headers = _homechef_signed_headers(cfg, "GET", path)
        if headers is None:
            return {
                "error": "identity_unverified",
                "_action_for_assistant": (
                    "Can't securely verify the customer to track their order. "
                    "Ask them to sign in and try again."
                ),
            }
        return await _get_json(
            cfg.homechef_api_url,
            path,
            source="homechef-api",
            headers=headers,
        )

    @mcp.tool(
        name="list_recent_orders",
        description=(
            "List a customer's HomeChef orders. Use `days` for a rolling window "
            "(default 14, MAX 30). For a specific date or span the customer "
            "names — 'what did I order on the 12th', 'my orders in July' — pass "
            "`date_from`/`date_to` as YYYY-MM-DD instead; both ends are "
            "inclusive, and passing the same date for each returns that one "
            "day. Each order carries its payment status, total, refund amount "
            "and payment method, so answer payment questions from this rather "
            "than guessing. range_exceeded for longer rolling windows — surface "
            "that and offer a date range instead."
        ),
    )
    async def list_recent_orders(
        days: int = 14,
        date_from: str = "",
        date_to: str = "",
    ) -> dict[str, Any]:
        # The customer is taken from the verified conversation context (never a
        # model arg); the backend scopes /api/v1/orders to that user.
        path = "/api/v1/orders"
        params: dict[str, Any] = {"limit": 50}

        # An explicit range wins over the rolling window: the customer named a
        # date, so honouring `days` as well would silently re-narrow it.
        explicit = _clean_date(date_from), _clean_date(date_to)
        if any(explicit):
            if date_from and not explicit[0]:
                return _bad_date("date_from", date_from)
            if date_to and not explicit[1]:
                return _bad_date("date_to", date_to)
            if explicit[0]:
                params["from"] = explicit[0]
            if explicit[1]:
                params["to"] = explicit[1]
        else:
            clamped = _check_range(days, max_days=30)
            if isinstance(clamped, dict):
                return clamped | {
                    "source": "homechef-api",
                    "_action_for_assistant": (
                        "That window is too long for a rolling lookup. Ask the "
                        "customer which dates they mean and call this again "
                        "with date_from/date_to."
                    ),
                }
            params["since_days"] = clamped

        headers = _homechef_signed_headers(cfg, "GET", path)
        if headers is None:
            return {
                "error": "identity_unverified",
                "_action_for_assistant": (
                    "Can't securely verify the customer to list their orders. "
                    "Ask them to sign in and try again."
                ),
            }
        return await _get_json(
            cfg.homechef_api_url,
            path,
            source="homechef-api",
            params=params,
            headers=headers,
        )

    @mcp.tool(
        name="create_refund_request",
        description=(
            "File a refund/issue REQUEST for a HomeChef order. This does NOT "
            "issue a refund — it raises an order issue that the chef/admin must "
            "review and resolve before any money is returned. Use after looking "
            "up the order (get_order_status). Put your investigation summary "
            "(what went wrong, eligibility, recommendation) in `findings`."
        ),
    )
    async def create_refund_request(
        order_id: str,
        reason: str,
        findings: str = "",
    ) -> dict[str, Any]:
        path = f"/api/v1/orders/{order_id}/report-issue"
        form = {"reason": reason, "description": findings or reason}
        content = urlencode(form).encode()
        headers = _homechef_signed_headers(cfg, "POST", path, content)
        if headers is None:
            return {
                "error": "identity_unverified",
                "_action_for_assistant": (
                    "Can't securely verify the customer to file this request. "
                    "Ask them to sign in and try again."
                ),
            }
        headers["Content-Type"] = "application/x-www-form-urlencoded"
        result = await _post(
            cfg.homechef_api_url,
            path,
            source="homechef-api",
            content=content,
            headers=headers,
        )
        if "error" not in result:
            result["_status"] = "pending_admin_approval"
            result["_action_for_assistant"] = (
                "Tell the customer their refund request has been raised as an "
                "order issue and is awaiting review — no refund is issued until "
                "the chef/admin approves it."
            )
        return result


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

    @mcp.tool(
        name="list_recent_trades",
        description=(
            "Trades on the user's account in the last N days. days defaults "
            "to 7, MAX 30. Longer windows return range_exceeded — surface "
            "that and ask the customer to raise a support ticket for the "
            "full history."
        ),
    )
    async def list_recent_trades(account_id: str, days: int = 7) -> dict[str, Any]:
        clamped = _check_range(days, max_days=30)
        if isinstance(clamped, dict):
            return clamped | {"source": "stockpilot-api", "_disclaimer": _DISCLAIMER}
        result = await _get_json(
            cfg.stockpilot_api_url,
            f"/api/portfolios/{account_id}/trades",
            source="stockpilot-api",
            params={"since_days": clamped, "limit": 100},
        )
        result.setdefault("_disclaimer", _DISCLAIMER)
        return result


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
        description=(
            "Recent matches for a user in the last N days. days defaults "
            "to 7, MAX 30 (anything longer asks the customer to open a "
            "support ticket). NOT YET WIRED — gameverse-server stores "
            "matches in PostgreSQL but doesn't expose them over HTTP."
        ),
    )
    async def get_match_history(user_id: str, days: int = 7) -> dict[str, Any]:
        clamped = _check_range(days, max_days=30)
        if isinstance(clamped, dict):
            return clamped | {"source": "gameverse-server"}
        return _not_implemented(
            "get_match_history",
            "gameverse-server stores matches in PostgreSQL but doesn't expose them over HTTP.",
        ) | {"user_id": user_id, "days_requested": clamped}


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
        description=(
            "Recent visual readings (palm/face/chart) on file for the user. "
            "Pass days to scope (default 30, MAX 60). Longer windows return "
            "range_exceeded — surface that and ask the customer to raise a "
            "support ticket."
        ),
    )
    async def list_recent_readings(user_id: str, days: int = 30, limit: int = 10) -> dict[str, Any]:
        clamped = _check_range(days, max_days=60)
        if isinstance(clamped, dict):
            return clamped | {"source": "horoscope-api", "_disclaimer": _DISCLAIMER}
        result = await _get_json(
            cfg.horoscope_api_url,
            "/api/v1/me/readings/visual",
            source="horoscope-api",
            params={"since_days": clamped, "limit": max(1, min(limit, 25))},
            headers={"X-User-Id": user_id} if user_id else None,
        )
        result.setdefault("_disclaimer", _DISCLAIMER)
        return result


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
        name="list_recent_scrape_jobs",
        description=(
            "Scrape jobs created in the last N days. days defaults to 7, "
            "MAX 30. Longer windows return range_exceeded — surface that "
            "and ask the customer to raise a support ticket for the full "
            "job history."
        ),
    )
    async def list_recent_scrape_jobs(days: int = 7, limit: int = 50) -> dict[str, Any]:
        clamped = _check_range(days, max_days=30)
        if isinstance(clamped, dict):
            return clamped | {"source": "scrapper-api"}
        return await _get_json(
            cfg.scrapper_api_url,
            "/api/jobs",
            source="scrapper-api",
            params={"since_days": clamped, "limit": max(1, min(limit, 100))},
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


# ---------------------------------------------------------------------------
# platform — tesserix-home (the "company" marketing/admin app)
#
# tesserix-home has no per-store order data, so this tenant's tools cover
# what the company site can actually answer for a visitor: product/FAQ
# lookups (via the shared knowledge base) and capturing a sales/contact
# lead. The contact endpoint is public (no auth), so the AI can file a
# lead without any customer session.
# ---------------------------------------------------------------------------
def _register_platform(mcp, cfg: Config) -> None:
    @mcp.tool(
        name="submit_contact_lead",
        description=(
            "Capture a sales/contact lead from a tesserix.app visitor when they "
            "ask to be contacted, request a demo, or want pricing follow-up. "
            "Pass their name, email and the message/intent. Returns a "
            "confirmation. Use only with details the visitor actually gave — "
            "never invent contact info."
        ),
    )
    async def submit_contact_lead(
        name: str,
        email: str,
        message: str,
        company: str = "",
    ) -> dict[str, Any]:
        body = {"name": name, "email": email, "message": message, "company": company}
        result = await _post(
            cfg.tesserix_home_url,
            "/api/contact",
            source="tesserix-home",
            json_body=body,
        )
        if "error" not in result:
            result["_action_for_assistant"] = (
                "Confirm to the visitor that the team will reach out to the email "
                "they gave. Do not promise a specific time."
            )
        return result

    @mcp.tool(
        name="get_platform_overview",
        description=(
            "High-level facts about the Tesserix platform + Mark8ly for answering "
            "'what is this / what can it do / how do I start' questions. Static, "
            "non-personal company info — safe to share with any visitor."
        ),
    )
    async def get_platform_overview() -> dict[str, Any]:
        return {
            "source": "tesserix-home",
            "company": "Tesserix",
            "summary": (
                "Tesserix builds commerce infrastructure. Mark8ly is its "
                "multi-tenant marketplace platform — merchants launch a branded "
                "storefront + admin in days."
            ),
            "products": ["Mark8ly (marketplace)", "HomeChef / fe3dr (food delivery)"],
            "get_started": "https://tesserix.app — use the contact form for a demo.",
            "_action_for_assistant": (
                "Answer company/marketing questions from these facts. For anything "
                "specific (pricing details, a demo), offer submit_contact_lead. Use "
                "search_knowledge_base for FAQ/policy detail when available."
            ),
        }


__all__ = ["register"]
