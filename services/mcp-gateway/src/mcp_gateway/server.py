"""Per-product tools hosted by the Tesserix stateless MCP runtime."""

from __future__ import annotations

import contextvars
import hashlib
import hmac
import inspect
import json
import logging
import secrets
from collections.abc import Callable, Mapping
from dataclasses import dataclass
from typing import Any

from mcp.shared.exceptions import MCPError
from mcp.types import INVALID_PARAMS
from tesserix_mcp_runtime import AuthenticatedIdentity, CallContext, Cancellation, JsonValue
from tesserix_mcp_runtime.adapters.streamable_http import (
    HTTPRequestAuthenticationError,
    HTTPRequestMetadata,
    ProtocolCallResult,
    ProtocolTelemetryEvent,
    ProtocolToolDescriptor,
    StreamableHTTPConfig,
    StreamableHTTPLimits,
    StreamableHTTPListener,
    StreamableHTTPTransport,
)

from . import auto_tools, shared_tools, tenants
from .config import Config, load

logger = logging.getLogger(__name__)

_PROTOCOL_VERSION = "2026-07-28"
_TRUSTED_HEADERS: dict[str, str] = {
    "X-Otto-Conversation-Id": "conversation_id",
    "X-Tenant-Id": "tenant_id",
    "X-Store-Id": "store_id",
    "X-Customer-Id": "customer_id",
    "X-Customer-Email": "customer_email",
    "X-Customer-Name": "customer_name",
    "X-Otto-Case-Id": "case_id",
}

request_ctx: contextvars.ContextVar[dict[str, str] | None] = contextvars.ContextVar(
    "mcp_request_ctx", default=None
)


class ToolRegistry:
    """Collect the existing tenant callables behind one protocol-neutral surface."""

    def __init__(self, *, allow_mutations: bool = False) -> None:
        self._tools: dict[str, dict[str, Any]] = {}
        self._allow_mutations = allow_mutations

    def tool(
        self, *, name: str, description: str = "", mutating: bool = False
    ) -> Callable[[Callable[..., Any]], Callable[..., Any]]:
        def decorator(fn: Callable[..., Any]) -> Callable[..., Any]:
            # Mutating tools stay unregistered unless MCP_ALLOW_MUTATIONS opts in.
            if mutating and not self._allow_mutations:
                return fn
            self._tools[name] = {
                "fn": fn,
                "description": description,
                "input_schema": _build_input_schema(fn),
            }
            return fn

        return decorator

    def list_tools(self) -> list[dict[str, Any]]:
        return [
            {
                "name": name,
                "description": entry["description"],
                "inputSchema": entry["input_schema"],
            }
            for name, entry in self._tools.items()
        ]

    def protocol_tools(self) -> tuple[ProtocolToolDescriptor, ...]:
        descriptors: list[ProtocolToolDescriptor] = []
        for tool in self.list_tools():
            description = " ".join(str(tool["description"]).split()) or f"Invoke {tool['name']}."
            description = description[:4096].rstrip()
            fingerprint = hashlib.sha256(
                json.dumps(
                    {
                        "name": tool["name"],
                        "description": description,
                        "inputSchema": tool["inputSchema"],
                    },
                    ensure_ascii=False,
                    separators=(",", ":"),
                    sort_keys=True,
                ).encode()
            ).hexdigest()
            descriptors.append(
                ProtocolToolDescriptor(
                    name=tool["name"],
                    description=description,
                    input_schema=tool["inputSchema"],
                    output_schema=None,
                    fingerprint=f"sha256:{fingerprint}",
                )
            )
        return tuple(descriptors)

    async def call(self, name: str, arguments: Mapping[str, JsonValue]) -> Any:
        if name not in self._tools:
            raise KeyError(name)
        fn = self._tools[name]["fn"]
        result = fn(**dict(arguments))
        if inspect.isawaitable(result):
            result = await result
        return result


def _build_input_schema(fn: Callable[..., Any]) -> dict[str, Any]:
    sig = inspect.signature(fn)
    properties: dict[str, Any] = {}
    required: list[str] = []
    for pname, param in sig.parameters.items():
        prop: dict[str, Any] = {"type": _py_type(param.annotation)}
        if param.default is inspect.Parameter.empty:
            required.append(pname)
        else:
            prop["default"] = param.default
        properties[pname] = prop
    return {
        "type": "object",
        "properties": properties,
        "required": required,
        "additionalProperties": False,
    }


_PY_TO_JSON: dict[type, str] = {
    str: "string",
    int: "integer",
    float: "number",
    bool: "boolean",
    list: "array",
    dict: "object",
}


def _py_type(annotation: Any) -> str:
    if annotation is inspect.Parameter.empty:
        return "string"
    return _PY_TO_JSON.get(annotation, "string")


class GatewayContextProvider:
    """Authenticate the shared router key before the runtime parses JSON-RPC."""

    def __init__(self, cfg: Config) -> None:
        self._cfg = cfg

    async def create(
        self,
        request: HTTPRequestMetadata,
        *,
        cancellation: Cancellation,
    ) -> CallContext:
        request_id = _one_header(request, "x-request-id") or f"mcp-{secrets.token_hex(16)}"
        provided = request.header_values("x-mcp-key")
        if self._cfg.auth_key is not None and (
            len(provided) != 1 or not hmac.compare_digest(provided[0], self._cfg.auth_key)
        ):
            raise HTTPRequestAuthenticationError(request_id=request_id)

        method = _one_header(request, "mcp-method")
        trusted = {
            field: value
            for header, field in _TRUSTED_HEADERS.items()
            if (value := _one_header(request, header)) is not None
        }
        if method == "tools/call" and trusted.get("tenant_id") != self._cfg.tenant:
            raise HTTPRequestAuthenticationError(request_id=request_id)
        request_ctx.set(trusted)

        run_id = trusted.get("conversation_id", request_id)
        subject = trusted.get("customer_id", "slm-router")
        return CallContext(
            identity=AuthenticatedIdentity(
                tenant=self._cfg.tenant,
                subject=subject,
                issuer="tesserix://slm-router",
                scopes=("mcp:invoke",),
            ),
            request_id=request_id,
            run_id=run_id,
            cancellation=cancellation,
            idempotency_key=_one_header(request, "x-idempotency-key"),
            approval_id=_one_header(request, "x-approval-id"),
        )


def _one_header(request: HTTPRequestMetadata, name: str) -> str | None:
    values = request.header_values(name)
    if len(values) != 1 or not values[0] or len(values[0]) > 256:
        return None
    return values[0]


class NullTelemetry:
    def emit(self, event: ProtocolTelemetryEvent) -> None:
        del event


class GatewayProtocolSession:
    def __init__(self, endpoint: GatewayEndpoint, context: CallContext) -> None:
        self._endpoint = endpoint
        self._context = context

    async def initialize(self) -> None:
        return None

    async def list_tools(self) -> tuple[ProtocolToolDescriptor, ...]:
        return self._endpoint.protocol_tools()

    async def call_tool(
        self,
        name: str,
        arguments: Mapping[str, JsonValue],
        *,
        meta: Mapping[str, JsonValue],
    ) -> ProtocolCallResult:
        del meta, self._context
        try:
            result = await self._endpoint.registry.call(name, arguments)
        except KeyError:
            raise MCPError(INVALID_PARAMS, "Unknown tool") from None
        if not isinstance(result, dict):
            result = {"result": result}
        encoded = json.dumps(result, ensure_ascii=False, separators=(",", ":"), sort_keys=True)
        return ProtocolCallResult(
            content=({"type": "text", "text": encoded},),
            structured_content=result,
            is_error=False,
        )

    async def close(self) -> None:
        return None


class GatewayEndpoint:
    def __init__(self, registry: ToolRegistry) -> None:
        self.registry = registry
        self._tools = registry.protocol_tools()

    def protocol_tools(self) -> tuple[ProtocolToolDescriptor, ...]:
        return self._tools

    def connect(self, *, context: CallContext, protocol_version: str) -> GatewayProtocolSession:
        if protocol_version != _PROTOCOL_VERSION:
            raise MCPError(INVALID_PARAMS, "Unsupported protocol version")
        return GatewayProtocolSession(self, context)


@dataclass(slots=True)
class GatewayRuntime:
    transport: StreamableHTTPTransport
    endpoint: GatewayEndpoint

    async def start(self) -> None:
        await self.transport.start(self.endpoint)

    async def drain(self) -> None:
        await self.transport.drain(deadline=0.0)

    async def stop(self) -> None:
        await self.transport.stop()


def build_registry(cfg: Config) -> ToolRegistry:
    registry = ToolRegistry(allow_mutations=cfg.allow_mutations)
    shared_tools.register(registry, cfg)
    tenants.register(registry, cfg)
    auto_count = auto_tools.register(registry, cfg)
    if auto_count:
        logger.info("auto-registered %d openapi tool(s) for tenant=%s", auto_count, cfg.tenant)
    return registry


def build_runtime(
    cfg: Config | None = None,
    *,
    listener: StreamableHTTPListener | None = None,
) -> GatewayRuntime:
    resolved = load() if cfg is None else cfg
    registry = build_registry(resolved)
    logger.info(
        "registered %d tools for tenant=%s: %s",
        len(registry.list_tools()),
        resolved.tenant,
        [tool["name"] for tool in registry.list_tools()],
    )
    transport = StreamableHTTPTransport(
        config=StreamableHTTPConfig(
            host=resolved.bind_host,
            port=resolved.bind_port,
            allowed_hosts=resolved.allowed_hosts,
            allowed_origins=resolved.allowed_origins,
        ),
        limits=StreamableHTTPLimits(
            max_request_body_bytes=65_536,
            max_response_body_bytes=524_288,
            max_schema_bytes=262_144,
            max_tools=128,
            tool_page_size=128,
            max_tool_pages=1,
            max_stream_seconds=30.0,
        ),
        context_provider=GatewayContextProvider(resolved),
        telemetry=NullTelemetry(),
        listener=listener,
    )
    return GatewayRuntime(transport=transport, endpoint=GatewayEndpoint(registry))


__all__ = ["GatewayRuntime", "ToolRegistry", "build_registry", "build_runtime", "request_ctx"]
