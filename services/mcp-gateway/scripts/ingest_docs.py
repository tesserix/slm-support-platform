#!/usr/bin/env python3
"""Ingest markdown docs into pgvector for the `search_knowledge_base`
MCP tool.

Usage
-----

    python scripts/ingest_docs.py --tenant fanzone --docs-dir docs/fanzone

Falls back to env vars (matching the mcp-gateway runtime config) if
flags are omitted:

    MCP_TENANT, VECTOR_DB_DSN, EMBEDDER_URL, INGEST_DOCS_DIR

The script:
  1. Reads every `*.md` under the docs dir.
  2. Splits frontmatter, chunks each body into ~200-word windows.
  3. Embeds each chunk via the embedder service.
  4. Replaces all rows for (namespace, source) in the `chunks` table
     so re-running the script is idempotent.

It writes nothing outside the database, and never deletes another
tenant's chunks — `namespace=<tenant>` is enforced on every query.
"""
from __future__ import annotations

import argparse
import asyncio
import logging
import os
import sys
from pathlib import Path

import psycopg

from mcp_gateway import ingest


logger = logging.getLogger("ingest_docs")


def parse_args() -> argparse.Namespace:
    p = argparse.ArgumentParser(
        description="Ingest markdown docs into pgvector for the per-tenant knowledge base.",
    )
    p.add_argument(
        "--tenant",
        default=os.environ.get("MCP_TENANT"),
        help="Tenant slug (becomes `namespace` in the chunks table). Defaults to $MCP_TENANT.",
    )
    p.add_argument(
        "--docs-dir",
        default=os.environ.get("INGEST_DOCS_DIR"),
        help=(
            "Root directory of markdown files. Defaults to $INGEST_DOCS_DIR, "
            "then to ./docs/<tenant>/ relative to the cwd."
        ),
    )
    p.add_argument(
        "--vector-db-dsn",
        default=os.environ.get("VECTOR_DB_DSN"),
        help="Postgres DSN with pgvector. Defaults to $VECTOR_DB_DSN.",
    )
    p.add_argument(
        "--embedder-url",
        default=os.environ.get("EMBEDDER_URL"),
        help="Base URL of the embedder service. Defaults to $EMBEDDER_URL.",
    )
    p.add_argument(
        "--actor",
        default=os.environ.get("INGEST_ACTOR", "ingest_docs.py"),
        help="Label written to the chunk_audit table. Useful for distinguishing CI vs. manual runs.",
    )
    p.add_argument(
        "--dry-run",
        action="store_true",
        help="Read+chunk only — don't embed and don't touch the database.",
    )
    p.add_argument(
        "--verbose",
        "-v",
        action="store_true",
        help="DEBUG-level logging.",
    )
    return p.parse_args()


async def main() -> int:
    args = parse_args()
    logging.basicConfig(
        level=logging.DEBUG if args.verbose else logging.INFO,
        format="%(asctime)s %(levelname)s %(name)s %(message)s",
    )

    tenant = (args.tenant or "").strip().lower()
    if not tenant:
        logger.error("--tenant (or $MCP_TENANT) is required")
        return 2

    docs_dir = Path(args.docs_dir) if args.docs_dir else Path("docs") / tenant
    if not docs_dir.exists():
        logger.error("docs dir %s does not exist", docs_dir)
        return 2

    files = list(ingest.iter_markdown_files(docs_dir))
    if not files:
        logger.warning("no markdown files found under %s — nothing to ingest", docs_dir)
        return 0
    logger.info("scanning %d markdown files under %s for tenant=%s", len(files), docs_dir, tenant)

    # First pass: load + chunk every file. This catches frontmatter
    # or path errors before we hit the embedder, so a bad file
    # doesn't waste an embedding API call.
    docs_chunks: list[tuple[ingest.Doc, list[ingest.Chunk]]] = []
    for file in files:
        doc = ingest.load_doc(file, root=docs_dir)
        chunks = ingest.chunk_doc(doc, namespace=tenant)
        if not chunks:
            logger.warning("file %s produced 0 chunks (empty?)", file)
            continue
        logger.debug("%s → %d chunks", doc.source, len(chunks))
        docs_chunks.append((doc, chunks))

    if args.dry_run:
        total_chunks = sum(len(c) for _, c in docs_chunks)
        logger.info("dry-run: would have ingested %d chunks across %d files", total_chunks, len(docs_chunks))
        return 0

    if not args.vector_db_dsn:
        logger.error("--vector-db-dsn (or $VECTOR_DB_DSN) is required when not --dry-run")
        return 2
    if not args.embedder_url:
        logger.error("--embedder-url (or $EMBEDDER_URL) is required when not --dry-run")
        return 2

    # Second pass: embed + upsert each file's chunks. We commit per
    # file rather than per run so a transient failure halfway through
    # a 50-file ingest doesn't roll back the work already done.
    total_inserted = 0
    total_deleted = 0
    with psycopg.connect(args.vector_db_dsn) as conn:
        for doc, chunks in docs_chunks:
            contents = [c.content for c in chunks]
            embeddings = await ingest.embed_chunks(args.embedder_url, contents)
            counts = ingest.upsert_chunks(
                conn,
                namespace=tenant,
                source=doc.source,
                chunks=chunks,
                embeddings=embeddings,
                actor=args.actor,
            )
            total_inserted += counts["inserted"]
            total_deleted += counts["deleted"]
            logger.info(
                "%-50s deleted=%d inserted=%d",
                doc.source,
                counts["deleted"],
                counts["inserted"],
            )

    logger.info(
        "done — tenant=%s files=%d chunks_deleted=%d chunks_inserted=%d",
        tenant, len(docs_chunks), total_deleted, total_inserted,
    )
    return 0


if __name__ == "__main__":
    sys.exit(asyncio.run(main()))
