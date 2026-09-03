"""Cross-tenant tools — knowledge-base search + conversation lookup.

Both are infrastructure-level (no product knowledge) so they're always
available no matter which tenant the gateway is serving.
"""
from __future__ import annotations

import json
import logging
from typing import Any

import httpx

from .config import Config

logger = logging.getLogger(__name__)


def register(mcp, cfg: Config) -> None:
    """Register the cross-tenant tools on the MCP tool registry."""

    @mcp.tool(
        name="search_knowledge_base",
        description=(
            "Semantic search of this product's knowledge base "
            "(FAQs, policy pages, product docs). Returns the top-k "
            "most relevant chunks with their source metadata."
        ),
    )
    async def search_knowledge_base(query: str, limit: int = 4) -> dict[str, Any]:
        if not cfg.vector_db_dsn or not cfg.embedder_url:
            return {
                "tenant": cfg.tenant,
                "query": query,
                "results": [],
                "error": "knowledge base not configured for this tenant",
                "_stub": True,
            }
        # Embed the query.
        async with httpx.AsyncClient(timeout=10) as client:
            r = await client.post(
                f"{cfg.embedder_url}/embed", json={"inputs": [query]}
            )
            r.raise_for_status()
            embedding: list[float] = r.json()[0]

        # pgvector cosine search, namespace-scoped.
        try:
            import psycopg

            with psycopg.connect(cfg.vector_db_dsn) as conn:
                with conn.cursor() as cur:
                    cur.execute(
                        """
                        SELECT id, content, metadata, embedding <=> %s::vector AS distance
                          FROM chunks
                         WHERE namespace = %s
                      ORDER BY embedding <=> %s::vector
                         LIMIT %s
                        """,
                        (_vector_literal(embedding), cfg.tenant, _vector_literal(embedding), limit),
                    )
                    rows = [
                        {
                            "id": r[0],
                            "content": r[1],
                            "metadata": r[2] or {},
                            "distance": float(r[3]),
                        }
                        for r in cur.fetchall()
                    ]
        except Exception as exc:  # pragma: no cover - integration only
            logger.exception("kb search failed")
            return {
                "tenant": cfg.tenant,
                "query": query,
                "results": [],
                "error": f"kb search failed: {exc}",
                "_stub": True,
            }

        return {
            "tenant": cfg.tenant,
            "query": query,
            "results": rows,
        }

    @mcp.tool(
        name="lookup_conversation",
        description=(
            "Fetch the message history of an Otto conversation. Use when "
            "the customer references something earlier in the thread "
            "and the agent needs to confirm what was said."
        ),
    )
    async def lookup_conversation(conversation_id: str, limit: int = 20) -> dict[str, Any]:
        if not cfg.mongo_url:
            return {
                "conversation_id": conversation_id,
                "messages": [],
                "error": "mongo not configured",
                "_stub": True,
            }
        try:
            import pymongo  # type: ignore[import-not-found]

            client = pymongo.MongoClient(cfg.mongo_url, serverSelectionTimeoutMS=5000)
            db = client[cfg.mongo_db]
            cur = (
                db.messages.find({"conversation_id": conversation_id})
                .sort("created_at", pymongo.ASCENDING)
                .limit(limit)
            )
            msgs = [
                {
                    "role": m.get("sender_type"),
                    "body": m.get("body", ""),
                    "at": _iso(m.get("created_at")),
                }
                for m in cur
            ]
        except Exception as exc:  # pragma: no cover
            logger.exception("conversation lookup failed")
            return {
                "conversation_id": conversation_id,
                "messages": [],
                "error": f"lookup failed: {exc}",
                "_stub": True,
            }
        return {"conversation_id": conversation_id, "messages": msgs}


def _vector_literal(vec: list[float]) -> str:
    return "[" + ",".join(f"{x:.6g}" for x in vec) + "]"


def _iso(value: Any) -> str | None:
    if value is None:
        return None
    try:
        return value.isoformat()
    except AttributeError:
        return str(value)


__all__ = ["register"]


_ = json  # keep import for future structured-output use
