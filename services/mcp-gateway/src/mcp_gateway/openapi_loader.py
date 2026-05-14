"""OpenAPI spec loading + parsing for auto-generated MCP tools.

Each product backend optionally serves an OpenAPI 3.x spec. The
`mcp-gateway` pulls those specs at startup and exposes every operation
that opts in via the `x-mcp-expose: customer-read` extension as an MCP
tool — so an SLM can answer any query the product's customer-facing
API can answer, not just the handful we hand-coded in `tenants.py`.

Two responsibilities:

1.  Fetch and validate spec documents (network, JSON parse, version
    sniff) — `fetch_spec`.
2.  Walk the spec and pick out the operations the gateway is allowed
    to expose, normalising them to a small dict the auto-tool
    registration code can consume — `extract_exposed_operations`.

Both stages are deliberately fail-soft: a missing or malformed spec
logs a warning and yields zero operations, never an exception. The
gateway pod must keep starting even when a backend is down.
"""
from __future__ import annotations

import logging
from dataclasses import dataclass, field
from typing import Any

import httpx


logger = logging.getLogger(__name__)


# Extension name and the allowlist value that opts an operation in.
# Keeping it explicit (not a boolean) leaves room for future audiences
# like "staff-read" or "admin-read" without breaking semantics.
MCP_EXPOSE_EXTENSION = "x-mcp-expose"
EXPOSE_CUSTOMER_READ = "customer-read"

# Spec fetch timeout — short enough that a dead backend doesn't stall
# pod startup for minutes, long enough to absorb a cold-start Knative
# pod on the other side.
_FETCH_TIMEOUT_SECONDS = 5.0


@dataclass(frozen=True)
class ExposedOperation:
    """A single operation the gateway is willing to expose as a tool.

    Carries everything `auto_tools.register` needs to synthesise the
    tool — name, description, the HTTP call shape, and the parameter
    schema (kept as the raw OpenAPI parameter list because the input
    schema generator in `auto_tools` understands that shape directly).
    """

    operation_id: str
    method: str  # always uppercase, e.g. "GET"
    path: str  # OpenAPI path with `{param}` placeholders intact
    summary: str
    description: str
    parameters: list[dict[str, Any]] = field(default_factory=list)
    base_url: str = ""  # which backend's spec this came from


async def fetch_spec(url: str) -> dict[str, Any] | None:
    """Fetch an OpenAPI document. Returns the parsed dict, or None on
    any failure (network, non-2xx, non-JSON, wrong shape).

    Never raises — callers iterate over multiple backends and a single
    bad one must not crash startup.
    """
    try:
        async with httpx.AsyncClient(timeout=_FETCH_TIMEOUT_SECONDS) as client:
            res = await client.get(url)
    except httpx.HTTPError as exc:
        logger.warning("openapi: failed to fetch %s: %s", url, exc)
        return None

    if res.status_code != 200:
        logger.warning("openapi: %s returned %d", url, res.status_code)
        return None

    try:
        body = res.json()
    except ValueError:
        logger.warning("openapi: %s did not return JSON", url)
        return None

    return _validate_spec_shape(body, source=url)


def load_spec_from_file(path: str) -> dict[str, Any] | None:
    """Load a spec from a local file. Mirrors fetch_spec's fail-soft
    contract for the file path: a missing or malformed file logs a
    warning and yields None.

    Used to bootstrap auto-tools before a backend has stood up its
    own `/openapi.json` route — each tenant's spec is baked into the
    mcp-gateway image at `/app/openapi/<tenant>/spec.yaml`. Once a
    backend owns its contract, switch the deploy from `*_FILES` to
    `*_URLS` and delete the file — same loader, same downstream.

    Accepts both JSON (`.json`) and YAML (`.yaml`/`.yml`) by content
    sniffing: YAML files start with `openapi:` or `---`, JSON with `{`.
    """
    import json as _json

    try:
        with open(path, "r", encoding="utf-8") as fh:
            raw = fh.read()
    except OSError as exc:
        logger.warning("openapi: cannot read %s: %s", path, exc)
        return None

    stripped = raw.lstrip()
    body: Any
    if stripped.startswith("{"):
        try:
            body = _json.loads(raw)
        except _json.JSONDecodeError as exc:
            logger.warning("openapi: %s is not valid JSON: %s", path, exc)
            return None
    else:
        try:
            import yaml  # type: ignore[import-untyped]
        except ImportError:
            logger.warning("openapi: PyYAML not installed, cannot read %s", path)
            return None
        try:
            body = yaml.safe_load(raw)
        except yaml.YAMLError as exc:
            logger.warning("openapi: %s is not valid YAML: %s", path, exc)
            return None

    return _validate_spec_shape(body, source=path)


def _validate_spec_shape(body: Any, *, source: str) -> dict[str, Any] | None:
    """Common shape check shared by fetch_spec + load_spec_from_file.
    Returns the body when it looks like a 3.x OpenAPI doc, else None
    plus a warning."""
    if not isinstance(body, dict) or "paths" not in body:
        logger.warning("openapi: %s has no 'paths' key", source)
        return None

    # Accept 3.0.x and 3.1.x. Swagger 2.0 has a different structure and
    # we don't auto-translate; the backend should upgrade.
    version = str(body.get("openapi", ""))
    if not version.startswith("3."):
        logger.warning("openapi: %s is not OpenAPI 3.x (got %r)", source, version)
        return None

    return body


def extract_exposed_operations(
    spec: dict[str, Any],
    *,
    base_url: str = "",
) -> list[ExposedOperation]:
    """Walk a spec and return only the operations tagged with
    `x-mcp-expose: customer-read`.

    Operations missing an `operationId` are skipped (the operationId
    is what the SLM will see as the tool name, so without it the tool
    is unusable). Methods other than GET are skipped for now — write
    operations need a separate "confirm-before-call" flow we haven't
    built yet.
    """
    out: list[ExposedOperation] = []
    paths = spec.get("paths") or {}
    if not isinstance(paths, dict):
        return out

    for path, item in paths.items():
        if not isinstance(item, dict):
            continue
        # Path-level parameters apply to every operation in this path.
        path_params: list[dict[str, Any]] = []
        raw_path_params = item.get("parameters")
        if isinstance(raw_path_params, list):
            path_params = [p for p in raw_path_params if isinstance(p, dict)]

        for method, op in item.items():
            method_upper = method.upper()
            if method_upper != "GET":
                continue  # Phase 1: read-only operations.
            if not isinstance(op, dict):
                continue

            expose = op.get(MCP_EXPOSE_EXTENSION)
            if expose != EXPOSE_CUSTOMER_READ:
                continue

            operation_id = op.get("operationId")
            if not operation_id or not isinstance(operation_id, str):
                logger.warning(
                    "openapi: skipping %s %s — missing operationId",
                    method_upper, path,
                )
                continue

            summary = str(op.get("summary") or "").strip()
            description = str(op.get("description") or "").strip()

            # Merge path-level + operation-level params, keeping the
            # operation-level definition when names collide.
            op_params: list[dict[str, Any]] = []
            raw_op_params = op.get("parameters")
            if isinstance(raw_op_params, list):
                op_params = [p for p in raw_op_params if isinstance(p, dict)]
            seen: set[str] = {p["name"] for p in op_params if isinstance(p.get("name"), str)}
            merged = list(op_params)
            for p in path_params:
                name = p.get("name")
                if isinstance(name, str) and name not in seen:
                    merged.append(p)

            out.append(
                ExposedOperation(
                    operation_id=operation_id,
                    method=method_upper,
                    path=path,
                    summary=summary,
                    description=description,
                    parameters=merged,
                    base_url=base_url,
                )
            )

    return out


__all__ = [
    "EXPOSE_CUSTOMER_READ",
    "ExposedOperation",
    "MCP_EXPOSE_EXTENSION",
    "extract_exposed_operations",
    "fetch_spec",
    "load_spec_from_file",
]
