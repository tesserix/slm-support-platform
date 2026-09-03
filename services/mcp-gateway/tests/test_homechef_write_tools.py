from __future__ import annotations

import base64
import dataclasses
import hashlib
import hmac

import httpx
import respx

from mcp_gateway import tenants
from mcp_gateway.config import load
from mcp_gateway.server import ToolRegistry, request_ctx


@respx.mock
async def test_homechef_refund_request_is_customer_scoped_and_body_signed() -> None:
    key = b"homechef-test-key"
    cfg = dataclasses.replace(
        load(),
        tenant="homechef",
        homechef_api_url="https://homechef.test",
        homechef_bff_hmac_key=base64.b64encode(key).decode(),
    )
    registry = ToolRegistry(allow_mutations=True)
    tenants.register(registry, cfg)
    route = respx.post("https://homechef.test/api/v1/orders/order-1/report-issue").mock(
        return_value=httpx.Response(201, json={"id": "issue-1"})
    )
    request_ctx.set(
        {
            "conversation_id": "conversation-1",
            "tenant_id": "homechef",
            "customer_id": "customer-1",
            "customer_email": "chef@example.test",
        }
    )

    result = await registry.call(
        "create_refund_request",
        {"order_id": "order-1", "reason": "food_quality", "findings": "meal was cold"},
    )

    assert result["id"] == "issue-1"
    request = route.calls.last.request
    assert request.headers["X-User-Id"] == "customer-1"
    assert request.headers["X-User-Email"] == "chef@example.test"
    assert request.headers["Content-Type"] == "application/x-www-form-urlencoded"
    body_hash = hashlib.sha256(request.content).hexdigest()
    payload = (
        f"POST\n/api/v1/orders/order-1/report-issue\n{body_hash}\n{request.headers['X-Auth-Ts']}"
    )
    expected = hmac.new(key, payload.encode(), hashlib.sha256).hexdigest()
    assert request.headers["X-Internal-Auth"] == expected


async def test_homechef_refund_request_fails_closed_without_verified_identity() -> None:
    cfg = dataclasses.replace(load(), tenant="homechef", homechef_bff_hmac_key=None)
    registry = ToolRegistry(allow_mutations=True)
    tenants.register(registry, cfg)
    request_ctx.set({})

    result = await registry.call(
        "create_refund_request",
        {"order_id": "order-1", "reason": "food_quality"},
    )

    assert result["error"] == "identity_unverified"
