"""The request helpers must refuse a path whose segments came from the
model and are not safe to interpolate.

This is a regression guard for a real trap: httpx does not reject dot
segments, it COLLAPSES them, so an id of "../../admin" produces a
successful request against a DIFFERENT upstream route rather than an
error. Asserting on httpx's own behaviour first keeps this test honest —
if a future httpx starts rejecting instead, the premise below fails
loudly rather than the guard silently becoming redundant.
"""

import httpx
import pytest

from mcp_gateway.tenants import (
    _first_unsafe_segment,
    _get_json,
    _post,
    _unsafe_path_error,
)


def test_httpx_collapses_dot_segments_which_is_why_this_guard_exists():
    # The premise. Not testing our code — testing the assumption it rests on.
    # Two ".." pop "orders" then "s", landing on a real sibling route.
    collapsed = str(httpx.URL("https://h/api/v1/stores/s/orders/../../admin"))
    assert collapsed == "https://h/api/v1/stores/admin"
    # And the request SUCCEEDS at that route rather than erroring — which
    # is the whole danger: no exception ever surfaces to the caller.
    assert ".." not in collapsed


@pytest.mark.parametrize(
    "path,expected",
    [
        ("/api/v1/stores/s/orders/../../admin", ".."),
        ("/api/v1/stores/s/orders/.", "."),
        ("/api/v1/orders//track", ""),          # id arrived empty
        ("/api/v1/orders/", ""),                 # trailing empty
        ("/api/v1/stores/../x", ".."),
    ],
)
def test_rejects_unsafe_segments(path, expected):
    assert _first_unsafe_segment(path) == expected


@pytest.mark.parametrize(
    "path",
    [
        "/api/v1/storefront/stores/tesserix-store/orders/ord_123",
        "/api/v1/orders/ord-123/track",
        "/api/v1/chefs/chef.42",                 # a dot INSIDE a segment is fine
        "/api/v1/orders/..sneaky",               # only an exact ".." is a dot segment
        "/api/v1/orders/a..b",
        "/api/v1/orders/ord%2f123",              # httpx leaves %2f encoded — one segment
    ],
)
def test_allows_safe_paths(path):
    assert _first_unsafe_segment(path) is None


def test_leading_slash_is_not_an_empty_segment():
    # The path's own opening "/" splits to a leading "", which must not
    # be mistaken for an interpolated-empty id.
    assert _first_unsafe_segment("/api/v1/orders/ord_1") is None


@pytest.mark.asyncio
async def test_get_refuses_without_issuing_a_request(monkeypatch):
    def explode(*a, **k):  # pragma: no cover - must never run
        raise AssertionError("a request was issued for an unsafe path")

    monkeypatch.setattr(httpx.AsyncClient, "get", explode)
    result = await _get_json(
        "https://h", "/api/v1/orders/../../admin", source="mp-orders"
    )
    assert result["error"] == "bad_path_segment"
    assert result["source"] == "mp-orders"
    assert "_action_for_assistant" in result


@pytest.mark.asyncio
async def test_post_refuses_without_issuing_a_request(monkeypatch):
    # The POST path matters most: create_refund_request files a return.
    def explode(*a, **k):  # pragma: no cover - must never run
        raise AssertionError("a request was issued for an unsafe path")

    monkeypatch.setattr(httpx.AsyncClient, "post", explode)
    result = await _post(
        "https://h",
        "/api/v1/storefront/stores/s/orders/../../admin/returns",
        source="mp-orders",
        json_body={"reason": "x"},
    )
    assert result["error"] == "bad_path_segment"


def test_error_never_invites_a_retry_with_a_stripped_id():
    # A refusal that says "try again" without saying "do not modify the id"
    # invites the model to sanitise and re-send, which defeats the guard.
    msg = _unsafe_path_error("mp-orders", "..")["_action_for_assistant"]
    assert "do not" in msg.lower()
    assert "guess" in msg.lower()
