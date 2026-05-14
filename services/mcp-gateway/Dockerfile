# Single image for ALL tenants — MCP_TENANT env var selects the tool set.
# Same image is deployed into every product namespace.

FROM python:3.12-slim AS builder
WORKDIR /build
COPY pyproject.toml ./
COPY src/ ./src/
# Regular install (not editable) so the package + deps actually land
# at the target path. `--target` + `-e` produces a .egg-link that
# resolves to a path that doesn't exist in the runtime image.
RUN pip install --no-cache-dir --upgrade pip \
 && pip install --no-cache-dir --target /pkg .

FROM python:3.12-slim AS runtime
ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/pkg \
    MCP_HOST=0.0.0.0 \
    MCP_PORT=8765 \
    MCP_LOG_LEVEL=INFO

WORKDIR /app
COPY --from=builder /pkg /pkg
# Docs corpus + ingestion CLI ride along in the same image so the
# CronJob can run `python scripts/ingest_docs.py` without a separate
# build. The runtime mcp-gateway server doesn't touch these paths,
# but they're tiny and keeping them in the same image keeps the
# Kargo promotion atomic (server + docs + ingest move together).
COPY docs/ /app/docs/
COPY scripts/ /app/scripts/
# Per-tenant OpenAPI specs the runtime gateway loads when
# MCP_OPENAPI_FILES points at /app/openapi/<tenant>/spec.yaml.
# Each tenant has its own file so the same image works for every
# tenant (the deploy-time value picks the file). Once a backend
# stands up its own /openapi.json route, switch the env from
# MCP_OPENAPI_FILES to MCP_OPENAPI_URLS for that tenant and delete
# the file on the next image build.
COPY openapi/ /app/openapi/

# Read-only root + non-root uid — same conventions as the rest of
# the support-platform deployments.
RUN addgroup --system app && adduser --system --no-create-home --uid 10001 --ingroup app app
USER 10001:10001

EXPOSE 8765
ENTRYPOINT ["python", "-m", "mcp_gateway"]
