from __future__ import annotations

import dataclasses

import httpx
import respx

from mcp_gateway import tenants
from mcp_gateway.config import load
from mcp_gateway.server import ToolRegistry


def _registry(**overrides: object) -> ToolRegistry:
    cfg = dataclasses.replace(
        load(),
        tenant="mark8ly",
        mark8ly_orders_url="https://mark8ly.test",
        **overrides,
    )
    registry = ToolRegistry(allow_mutations=False)
    tenants.register(registry, cfg)
    return registry


# Matches StorefrontProductResponse verbatim (marketplace-api's
# internal/handlers/storefront/dto.go) — including price_range.min/max as
# JSON STRINGS (decimal.Decimal on the Go side) and media entries that
# aren't all images.
_RAW_PRODUCT = {
    "id": "internal-uuid-should-not-leak",
    "handle": "wool-jumper",
    "title": "Wool Jumper",
    "description": "Warm and cosy.",
    "tags": ["winter", "knitwear"],
    "categories": [
        {"name": "Knitwear", "slug": "knitwear"},
        {"name": "Winter", "slug": "winter"},
    ],
    "options": [{"name": "Size", "values": ["S", "M", "L"]}],
    "variants": [],
    "media": [
        {"url": "https://cdn.test/wool-jumper-2.jpg", "media_type": "image", "position": 2},
        {"url": "https://cdn.test/wool-jumper-spec.pdf", "media_type": "document", "position": 1},
        {"url": "https://cdn.test/wool-jumper-1.jpg", "media_type": "image", "position": 1},
    ],
    "price_range": {"min": "49.00", "max": "69.00", "currency_code": "INR"},
    "tax_code": "TX-1",
    "tax_rate_override": "0.18",
    "tax_category": "apparel",
    "published_at": "2026-01-01T00:00:00Z",
}


@respx.mock
async def test_list_store_products_projects_and_strips_tax_fields() -> None:
    registry = _registry()
    respx.get("https://mark8ly.test/api/v1/storefront/stores/tesserix-store/products").mock(
        return_value=httpx.Response(200, json={"data": [_RAW_PRODUCT], "meta": {"total": 1}})
    )

    result = await registry.call("list_store_products", {})

    assert "error" not in result
    (product,) = result["data"]
    assert product == {
        "handle": "wool-jumper",
        "title": "Wool Jumper",
        "description": "Warm and cosy.",
        "price_range": {"min": "49.00", "max": "69.00", "currency_code": "INR"},
        "categories": [
            {"name": "Knitwear", "slug": "knitwear"},
            {"name": "Winter", "slug": "winter"},
        ],
        "images": [
            "https://cdn.test/wool-jumper-1.jpg",
            "https://cdn.test/wool-jumper-2.jpg",
        ],
    }
    # Money must survive as strings — decimal.Decimal on the Go side. A
    # float would silently round a price.
    assert isinstance(product["price_range"]["min"], str)
    assert isinstance(product["price_range"]["max"], str)
    for leaked_field in ("id", "tax_code", "tax_rate_override", "tax_category"):
        assert leaked_field not in product
    assert result["meta"] == {"total": 1}


@respx.mock
async def test_list_store_products_sends_page_and_page_size_never_limit_offset() -> None:
    registry = _registry()
    route = respx.get(
        "https://mark8ly.test/api/v1/storefront/stores/tesserix-store/products"
    ).mock(return_value=httpx.Response(200, json={"data": []}))

    await registry.call("list_store_products", {"page": 3, "page_size": 500})

    request = route.calls.last.request
    assert request.url.params["page"] == "3"
    # page_size must be clamped to the backend's max=100.
    assert request.url.params["page_size"] == "100"
    assert "limit" not in request.url.params
    assert "offset" not in request.url.params


@respx.mock
async def test_list_store_products_omits_paging_params_when_not_supplied() -> None:
    registry = _registry()
    route = respx.get(
        "https://mark8ly.test/api/v1/storefront/stores/tesserix-store/products"
    ).mock(return_value=httpx.Response(200, json={"data": []}))

    await registry.call("list_store_products", {})

    request = route.calls.last.request
    assert "page" not in request.url.params
    assert "page_size" not in request.url.params


@respx.mock
async def test_get_store_product_returns_bare_projected_object() -> None:
    registry = _registry()
    respx.get(
        "https://mark8ly.test/api/v1/storefront/stores/tesserix-store/products/wool-jumper"
    ).mock(return_value=httpx.Response(200, json=_RAW_PRODUCT))

    result = await registry.call("get_store_product", {"handle": "wool-jumper"})

    assert result["handle"] == "wool-jumper"
    assert result["price_range"] == {"min": "49.00", "max": "69.00", "currency_code": "INR"}
    assert result["categories"] == [
        {"name": "Knitwear", "slug": "knitwear"},
        {"name": "Winter", "slug": "winter"},
    ]
    assert result["images"] == [
        "https://cdn.test/wool-jumper-1.jpg",
        "https://cdn.test/wool-jumper-2.jpg",
    ]
    for leaked_field in ("id", "tax_code", "tax_rate_override", "tax_category", "currency"):
        assert leaked_field not in result


@respx.mock
async def test_list_store_categories_returns_data_list() -> None:
    # Matches StorefrontCategoryResponse verbatim: {name, slug, position,
    # featured} — no product count field, whatever an OpenAPI doc might claim.
    registry = _registry()
    category = {"name": "Knitwear", "slug": "knitwear", "position": 1, "featured": True}
    respx.get("https://mark8ly.test/api/v1/storefront/stores/tesserix-store/categories").mock(
        return_value=httpx.Response(200, json={"data": [category]})
    )

    result = await registry.call("list_store_categories", {})

    assert result["data"] == [category]
    assert "product_count" not in result["data"][0]


@respx.mock
async def test_list_products_by_category_projects_products() -> None:
    registry = _registry()
    respx.get(
        "https://mark8ly.test/api/v1/storefront/stores/tesserix-store/categories/"
        "knitwear/products"
    ).mock(return_value=httpx.Response(200, json={"data": [_RAW_PRODUCT]}))

    result = await registry.call(
        "list_products_by_category", {"category_slug": "knitwear"}
    )

    (product,) = result["data"]
    assert product["categories"] == [
        {"name": "Knitwear", "slug": "knitwear"},
        {"name": "Winter", "slug": "winter"},
    ]
    assert product["images"] == [
        "https://cdn.test/wool-jumper-1.jpg",
        "https://cdn.test/wool-jumper-2.jpg",
    ]
    assert "tax_code" not in product


@respx.mock
async def test_get_store_branding_passes_through_active_promotion() -> None:
    registry = _registry()
    respx.get("https://mark8ly.test/api/v1/storefront/stores/tesserix-store/branding").mock(
        return_value=httpx.Response(
            200, json={"name": "Tesserix Store", "active_promotion": {"headline": "Sale"}}
        )
    )

    result = await registry.call("get_store_branding", {})

    assert result["active_promotion"] == {"headline": "Sale"}


@respx.mock
async def test_get_store_branding_omits_promotion_when_none_active() -> None:
    registry = _registry()
    respx.get("https://mark8ly.test/api/v1/storefront/stores/tesserix-store/branding").mock(
        return_value=httpx.Response(200, json={"name": "Tesserix Store"})
    )

    result = await registry.call("get_store_branding", {})

    assert "active_promotion" not in result


async def test_get_store_product_rejects_path_traversal_handle() -> None:
    registry = _registry()

    result = await registry.call("get_store_product", {"handle": "../secret"})

    assert result["error"] == "bad_path_segment"
    assert result["field"] == "handle"


async def test_list_store_products_rejects_bad_store_slug() -> None:
    registry = _registry()

    result = await registry.call("list_store_products", {"store_slug": "../other-store"})

    assert result["error"] == "bad_path_segment"
    assert result["field"] == "store_slug"


async def test_list_products_by_category_rejects_empty_category_slug() -> None:
    registry = _registry()

    result = await registry.call("list_products_by_category", {"category_slug": ""})

    assert result["error"] == "bad_path_segment"
    assert result["field"] == "category_slug"


@respx.mock
async def test_catalog_tools_use_plain_storefront_headers_not_customer_scoped() -> None:
    # Catalog reads are public store data — they must NOT require or send a
    # verified customer identity the way get_order does.
    registry = _registry(openapi_backend_headers={"X-Api-Key": "public-key"})
    route = respx.get(
        "https://mark8ly.test/api/v1/storefront/stores/tesserix-store/products"
    ).mock(return_value=httpx.Response(200, json={"data": []}))

    result = await registry.call("list_store_products", {})

    assert "error" not in result
    request = route.calls.last.request
    assert request.headers["X-Api-Key"] == "public-key"
    assert "X-Customer-Email" not in request.headers
