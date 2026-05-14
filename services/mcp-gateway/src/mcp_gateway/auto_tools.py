"""Auto-register MCP tools from product backend OpenAPI specs.

This is the bridge between `openapi_loader` (which knows how to read
specs) and `ToolRegistry` (which the SLM queries via JSON-RPC).

For every OpenAPI operation that's been tagged `x-mcp-expose:
customer-read`, we synthesise a tool with:

  * **name**       = the operation's `operationId`
  * **description** = the operation summary + description, capped so
                     tools/list responses stay small enough for a
                     local SLM to read without losing context
  * **input schema** = a JSON Schema built from the operation's
                     parameters list (path params + query params)
  * **call**       = an HTTP GET against the backend base URL with the
                     path params substituted and the rest forwarded as
                     query string

The tools follow the same trust model as the hand-rolled ones in
`tenants.py`: customer identity (user_id / email) is passed as a
regular argument that the SLM is instructed to fill from the system
prompt, never invented. Hardening that into mcp-gateway-side injection
of the X-User-* headers is tracked separately — it requires threading
per-request context into the tool call API which the current
`registry.call` signature doesn't support.

Like `openapi_loader`, this module is fail-soft: a backend with a
broken spec just contributes zero auto-tools.
"""
from __future__ import annotations

import logging
import re
from typing import Any
from urllib.parse import urljoin

import httpx

from .config import Config
from .openapi_loader import (
    ExposedOperation,
    extract_exposed_operations,
    fetch_spec,
    load_spec_from_file,
)


logger = logging.getLogger(__name__)


# Same socket-level budget the hand-rolled tools use, so the SLM has
# consistent latency expectations regardless of which path generated
# the tool.
_HTTP_TIMEOUT_SECONDS = 4.0

# Tools/list payloads get sent on every conversation. Cap description
# length so a 30-tool tenant doesn't push a small SLM past its
# context window for the system prompt.
_MAX_DESCRIPTION_CHARS = 320

# OpenAPI primitive types → JSON Schema types. We accept these
# verbatim from the spec into the MCP input schema.
_ALLOWED_SCHEMA_TYPES = {"string", "integer", "number", "boolean", "array", "object"}

# Path placeholders look like `/orders/{order_id}` — capture each
# `{name}` so we can substitute it from the call's arguments.
_PATH_PARAM_RE = re.compile(r"\{([^{}/]+)\}")


async def register(reg: Any, cfg: Config) -> int:
    """Pull every URL in `cfg.openapi_urls`, expose the tagged
    operations as tools on `reg`. Returns the count of tools
    registered (useful for the startup log line).

    Tools are registered with their operationId as the name. If two
    backends define the same operationId the later one wins; that's
    flagged as a warning since it's almost certainly a spec bug — but
    we don't refuse to start, since the SLM is still better off with
    SOMETHING than nothing.

    Backend calls include `cfg.openapi_backend_headers` on every
    request — used for things like the `X-Storefront-Key` shared
    secret mark8ly's storefront API requires. The headers are
    constant per pod (set via env var at deploy time), not derived
    from the customer context.
    """
    if not cfg.openapi_urls and not cfg.openapi_files:
        return 0

    registered = 0
    # URL-backed specs come first (live-served by a backend that
    # owns its own contract). File-backed specs are the bootstrap
    # path before a backend has stood that up — they sit behind
    # URL-based ones so a freshly-tagged real endpoint wins over a
    # stale checked-in YAML.
    for url in cfg.openapi_urls:
        spec = await fetch_spec(url)
        if spec is None:
            continue
        base_url = _server_url_from_spec(spec, fallback=url)
        for op in extract_exposed_operations(spec, base_url=base_url):
            if _register_one(reg, op, headers=cfg.openapi_backend_headers):
                registered += 1

    for path in cfg.openapi_files:
        spec = load_spec_from_file(path)
        if spec is None:
            continue
        # File specs have no fetch URL to fall back to for the base —
        # they must declare `servers[0].url` explicitly (typically
        # the in-cluster service DNS for that backend).
        base_url = _server_url_from_spec(spec, fallback="")
        for op in extract_exposed_operations(spec, base_url=base_url):
            if _register_one(reg, op, headers=cfg.openapi_backend_headers):
                registered += 1

    return registered


def _register_one(reg: Any, op: ExposedOperation, *, headers: dict[str, str] | None = None) -> bool:
    """Synthesise one tool from an operation and register it on `reg`.
    Returns True on success, False when the operation was skipped
    because it can't be safely synthesised (bad parameters, etc.).
    """
    description = _build_description(op)
    input_schema = _build_input_schema(op.parameters)
    if input_schema is None:
        logger.warning(
            "auto_tools: skipping %s — parameter schema could not be built",
            op.operation_id,
        )
        return False

    handler = _make_handler(op, headers=headers)
    # The decorator call surface ToolRegistry exposes is `tool(name=...,
    # description=...)`. The auto-generated input schema doesn't go
    # through `_build_input_schema(fn)` — the function signature would
    # be useless (we use **kwargs). Instead we patch the registry's
    # internal entry directly after decoration so the SLM sees the
    # OpenAPI-derived schema.
    reg.tool(name=op.operation_id, description=description)(handler)
    if hasattr(reg, "_tools") and op.operation_id in reg._tools:
        reg._tools[op.operation_id]["input_schema"] = input_schema
    return True


def _build_description(op: ExposedOperation) -> str:
    parts: list[str] = []
    if op.summary:
        parts.append(op.summary)
    if op.description and op.description != op.summary:
        parts.append(op.description)
    text = " — ".join(parts) if parts else f"{op.method} {op.path}"
    if len(text) > _MAX_DESCRIPTION_CHARS:
        text = text[: _MAX_DESCRIPTION_CHARS - 1].rstrip() + "…"
    return text


def _build_input_schema(parameters: list[dict[str, Any]]) -> dict[str, Any] | None:
    """Translate an OpenAPI parameters list into a JSON Schema the
    SLM can consume. Returns None if any parameter is malformed —
    we'd rather skip the tool than show the SLM something broken.
    """
    properties: dict[str, Any] = {}
    required: list[str] = []
    for p in parameters:
        name = p.get("name")
        loc = p.get("in")
        if not isinstance(name, str) or loc not in {"path", "query"}:
            # Header / cookie params aren't supported by Phase 1 —
            # they require special handling at call time. Skip the
            # whole tool rather than silently dropping the param.
            return None

        schema = p.get("schema") or {}
        if not isinstance(schema, dict):
            return None
        prop = _normalise_schema(schema)

        desc = p.get("description")
        if isinstance(desc, str) and desc.strip():
            prop["description"] = desc.strip()

        properties[name] = prop
        # Path params are implicitly required in OpenAPI; query params
        # opt in via `required: true`.
        if loc == "path" or p.get("required") is True:
            required.append(name)

    return {
        "type": "object",
        "properties": properties,
        "required": required,
    }


def _normalise_schema(schema: dict[str, Any]) -> dict[str, Any]:
    """Pick out the JSON Schema fields we trust to forward. Anything
    exotic ($ref to components, oneOf etc.) we replace with a permissive
    string default — Phase 1 stays simple."""
    s_type = schema.get("type")
    if not isinstance(s_type, str) or s_type not in _ALLOWED_SCHEMA_TYPES:
        return {"type": "string"}
    out: dict[str, Any] = {"type": s_type}
    if "enum" in schema and isinstance(schema["enum"], list):
        out["enum"] = list(schema["enum"])
    if "default" in schema:
        out["default"] = schema["default"]
    if s_type == "array":
        item_schema = schema.get("items")
        out["items"] = (
            _normalise_schema(item_schema) if isinstance(item_schema, dict) else {"type": "string"}
        )
    return out


def _make_handler(op: ExposedOperation, *, headers: dict[str, str] | None = None):
    """Build the async function the registry will invoke when the SLM
    calls this tool. Captures `op` by closure so each tool stays
    bound to the right URL/path/parameters. `headers` is passed
    through to httpx on every call — used to forward shared-secret
    headers like `X-Storefront-Key` that gate the backend routes
    behind a service-mesh-level trust check.
    """
    path_param_names = set(_PATH_PARAM_RE.findall(op.path))
    static_headers = dict(headers) if headers else None

    async def call_backend(**kwargs: Any) -> dict[str, Any]:
        # Substitute path params into the URL template.
        try:
            substituted = op.path.format(**{
                k: kwargs[k] for k in path_param_names if k in kwargs
            })
        except KeyError as exc:
            return {
                "error": "missing_argument",
                "detail": f"required path parameter {exc.args[0]!r} not supplied",
                "operation_id": op.operation_id,
            }
        missing = [name for name in path_param_names if name not in kwargs]
        if missing:
            return {
                "error": "missing_argument",
                "detail": f"required path parameter(s) {missing!r} not supplied",
                "operation_id": op.operation_id,
            }

        # Everything else becomes query string; skip Nones so we don't
        # send `?foo=None`.
        query: dict[str, Any] = {
            k: v
            for k, v in kwargs.items()
            if k not in path_param_names and v is not None
        }

        url = _join_url(op.base_url, substituted)
        try:
            async with httpx.AsyncClient(timeout=_HTTP_TIMEOUT_SECONDS) as client:
                res = await client.get(url, params=query, headers=static_headers)
        except httpx.HTTPError as exc:
            logger.warning("auto_tools: GET %s failed: %s", url, exc)
            return {
                "error": "backend_unreachable",
                "detail": str(exc),
                "operation_id": op.operation_id,
            }

        if 200 <= res.status_code < 300:
            try:
                body = res.json()
            except ValueError:
                body = {"raw": res.text[:500]}
            if isinstance(body, dict):
                return {**body, "_operation_id": op.operation_id}
            return {"data": body, "_operation_id": op.operation_id}

        return {
            "error": "lookup_failed",
            "status": res.status_code,
            "detail": res.text[:300] if res.text else None,
            "operation_id": op.operation_id,
        }

    # Give the handler a useful __name__ for tracebacks.
    call_backend.__name__ = f"auto_{op.operation_id}"
    return call_backend


def _server_url_from_spec(spec: dict[str, Any], *, fallback: str) -> str:
    """Pick the first `servers[].url` entry the spec advertises,
    falling back to the spec URL's origin if none is set.

    Backends behind in-cluster service DNS typically don't bother to
    declare a `servers` block, so the fallback is what gets used in
    practice — we strip `/openapi.json` (or similar) off the spec URL
    to recover the API root.
    """
    servers = spec.get("servers")
    if isinstance(servers, list) and servers:
        first = servers[0]
        if isinstance(first, dict):
            url = first.get("url")
            if isinstance(url, str) and url:
                return url.rstrip("/")
    # Strip the trailing path segment from the spec URL.
    if "/" in fallback:
        return fallback.rsplit("/", 1)[0]
    return fallback


def _join_url(base: str, path: str) -> str:
    if not base:
        return path
    if path.startswith("http://") or path.startswith("https://"):
        return path
    # urljoin would chop trailing `/api` off `base` if `path` is
    # absolute. We want path joining, not URL-style resolution.
    return base.rstrip("/") + "/" + path.lstrip("/")


__all__ = ["register"]


_ = urljoin  # kept for future absolute-URL handling
