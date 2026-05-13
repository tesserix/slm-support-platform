# Single image for ALL tenants — MCP_TENANT env var selects the tool set.
# Same image is deployed into every product namespace.

FROM python:3.12-slim AS builder
WORKDIR /build
COPY pyproject.toml ./
COPY src/ ./src/
RUN pip install --no-cache-dir --upgrade pip \
 && pip install --no-cache-dir --target /pkg -e .

FROM python:3.12-slim AS runtime
ENV PYTHONUNBUFFERED=1 \
    PYTHONPATH=/pkg \
    MCP_HOST=0.0.0.0 \
    MCP_PORT=8765 \
    MCP_LOG_LEVEL=INFO

WORKDIR /app
COPY --from=builder /pkg /pkg

# Read-only root + non-root uid — same conventions as the rest of
# the support-platform deployments.
RUN addgroup --system app && adduser --system --no-create-home --uid 10001 --ingroup app app
USER 10001:10001

EXPOSE 8765
ENTRYPOINT ["python", "-m", "mcp_gateway"]
