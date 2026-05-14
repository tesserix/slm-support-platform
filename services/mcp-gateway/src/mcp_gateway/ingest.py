"""Knowledge-base ingestion — markdown → pgvector.

The `search_knowledge_base` MCP tool (in `shared_tools.py`) does the
retrieval half: take a query, embed it, return the top-k chunks
from the `chunks` table filtered by `namespace=<tenant>`. This
module does the other half — populate that table.

Input: a directory of markdown files. Each file is one document
(an FAQ entry, a policy page, a help-center article). The file may
start with optional YAML-ish frontmatter:

    ---
    title: How points work
    source_url: https://fanzone.example/help/points
    tags: [points, scoring]
    ---

    Body of the article...

Output: rows in the shared `chunks` table with the right namespace
and embeddings, plus a row per change in `chunk_audit`. Both tables
are defined in
`tesserix-k8s/.../support-platform/support_platform.sql`.

Re-ingestion of the same file is idempotent: every run for a given
(namespace, source) deletes the previous chunks for that pair and
re-inserts. That keeps the table in lock-step with the source
markdown — edit a doc, re-run the script, the index reflects it
on the next query.
"""
from __future__ import annotations

import dataclasses
import hashlib
import json
import logging
import re
from pathlib import Path
from typing import Any, Iterable

import httpx


logger = logging.getLogger(__name__)


# Embedder service contract — matches what `shared_tools.search_knowledge_base`
# already calls. Keeping them in sync is what makes the indexed embeddings
# comparable to the runtime query embedding.
EMBED_PATH = "/embed"
EMBED_BATCH = 32

# Chunk size targets. The embedding model is sentence-transformers
# all-MiniLM-L6-v2 (the only widely-deployed model that emits the
# 384-dim vectors our `chunks.embedding VECTOR(384)` column expects),
# which has a 256-token context window. ~200 words per chunk lands
# comfortably inside that with English text.
TARGET_WORDS_PER_CHUNK = 200
MIN_WORDS_PER_CHUNK = 50  # avoid emitting a stub chunk for a one-line file


@dataclasses.dataclass(frozen=True)
class Doc:
    """Parsed markdown document — frontmatter + body. Source is the
    path relative to the docs root (used as the de-duplication key)."""

    source: str
    title: str
    body: str
    metadata: dict[str, Any]


@dataclasses.dataclass(frozen=True)
class Chunk:
    """One chunk ready to be embedded + stored. The ID is stable across
    re-ingestions for a given (namespace, source, index) so the audit
    trail can reason about updates rather than churn."""

    id: str
    namespace: str
    content: str
    metadata: dict[str, Any]


# ---------------------------------------------------------------------------
# Frontmatter parsing — tiny, deliberately not using pyyaml.
# ---------------------------------------------------------------------------
_FRONTMATTER_RE = re.compile(r"^---\s*\n(.+?)\n---\s*\n", re.DOTALL)
_LIST_RE = re.compile(r"^\[(.*)\]$")


def parse_frontmatter(raw: str) -> tuple[dict[str, Any], str]:
    """Pull a YAML-ish frontmatter block off the top of a markdown
    string. Returns (metadata, body). If no frontmatter, returns
    ({}, raw).

    Supports the few field shapes the seed docs actually use — string
    values and simple `[a, b, c]` lists. Anything more elaborate
    (nested dicts, multi-line strings) gets passed through as a string
    so we never crash on a doc the seed authors didn't anticipate.
    """
    match = _FRONTMATTER_RE.match(raw)
    if not match:
        return {}, raw
    block = match.group(1)
    metadata: dict[str, Any] = {}
    for line in block.splitlines():
        line = line.rstrip()
        if not line or line.startswith("#"):
            continue
        if ":" not in line:
            continue
        key, _, value = line.partition(":")
        key = key.strip()
        value = value.strip()
        # Strip matching quotes.
        if len(value) >= 2 and value[0] == value[-1] and value[0] in {'"', "'"}:
            value = value[1:-1]
        # Comma-list shorthand: tags: [a, b, c]
        list_match = _LIST_RE.match(value)
        if list_match:
            metadata[key] = [
                item.strip().strip('"').strip("'")
                for item in list_match.group(1).split(",")
                if item.strip()
            ]
        else:
            metadata[key] = value
    body = raw[match.end():]
    return metadata, body


def load_doc(path: Path, *, root: Path) -> Doc:
    """Read a markdown file and split out its frontmatter. `source`
    is the path relative to `root`, which makes the key portable
    across machines and is what the per-source upsert keys on."""
    raw = path.read_text(encoding="utf-8")
    metadata, body = parse_frontmatter(raw)
    title = str(metadata.get("title") or _derive_title(body) or path.stem)
    source = str(path.relative_to(root)).replace("\\", "/")
    return Doc(source=source, title=title, body=body.strip(), metadata=metadata)


def _derive_title(body: str) -> str | None:
    """Fall back to the first `# Heading` if frontmatter lacks an
    explicit title. Keeps the seed docs readable as plain markdown."""
    for line in body.splitlines():
        s = line.strip()
        if s.startswith("# "):
            return s[2:].strip()
    return None


# ---------------------------------------------------------------------------
# Chunking — paragraph-greedy packing into ~200-word chunks.
# ---------------------------------------------------------------------------
def chunk_doc(doc: Doc, *, namespace: str) -> list[Chunk]:
    """Split a doc's body into overlapping chunks suitable for
    embedding. Headings are kept with the paragraph they introduce
    rather than emitted as standalone chunks (a standalone heading
    has almost no semantic content).
    """
    paragraphs = _paragraphs(doc.body)
    if not paragraphs:
        return []

    packed: list[list[str]] = []
    current: list[str] = []
    current_words = 0
    for para in paragraphs:
        words = _word_count(para)
        # If we already have content and adding this paragraph would
        # exceed the target, flush current — unless current is so small
        # it would emit a stub chunk, in which case keep packing.
        if (
            current
            and current_words + words > TARGET_WORDS_PER_CHUNK
            and current_words >= MIN_WORDS_PER_CHUNK
        ):
            packed.append(current)
            current = []
            current_words = 0
        current.append(para)
        current_words += words
    if current:
        packed.append(current)

    out: list[Chunk] = []
    for idx, group in enumerate(packed):
        content = "\n\n".join(group).strip()
        if not content:
            continue
        out.append(
            Chunk(
                id=chunk_id(namespace, doc.source, idx),
                namespace=namespace,
                content=content,
                metadata={
                    "source": doc.source,
                    "title": doc.title,
                    "chunk_index": idx,
                    "chunk_count": len(packed),
                    **{k: v for k, v in doc.metadata.items() if k != "title"},
                },
            )
        )
    return out


def chunk_id(namespace: str, source: str, index: int) -> str:
    """Deterministic chunk ID so re-ingesting the same file overwrites
    rather than appending. Uses a short sha256 prefix to keep IDs
    indexable but readable in audit logs."""
    raw = f"{namespace}|{source}|{index:04d}".encode()
    return hashlib.sha256(raw).hexdigest()[:24]


def _paragraphs(body: str) -> list[str]:
    """Split a markdown body into paragraphs. Code fences (```...```)
    are kept intact as single units — splitting inside a code block
    is a recipe for breaking the SLM's reasoning over the snippet."""
    out: list[str] = []
    lines = body.splitlines()
    buf: list[str] = []
    in_fence = False
    for line in lines:
        if line.startswith("```"):
            in_fence = not in_fence
            buf.append(line)
            continue
        if in_fence:
            buf.append(line)
            continue
        if not line.strip():
            if buf:
                out.append("\n".join(buf).strip())
                buf = []
        else:
            buf.append(line)
    if buf:
        out.append("\n".join(buf).strip())
    return [p for p in out if p]


def _word_count(text: str) -> int:
    return len(text.split())


# ---------------------------------------------------------------------------
# Embedding — POST {url}/embed with batched inputs.
# ---------------------------------------------------------------------------
async def embed_chunks(embedder_url: str, contents: list[str]) -> list[list[float]]:
    """Send chunk text to the embedder service in batches of
    EMBED_BATCH. The service is the same one `search_knowledge_base`
    queries at runtime — feeding the index through the same model
    is what makes cosine-similarity work."""
    out: list[list[float]] = []
    async with httpx.AsyncClient(timeout=30) as client:
        for start in range(0, len(contents), EMBED_BATCH):
            batch = contents[start : start + EMBED_BATCH]
            res = await client.post(f"{embedder_url}{EMBED_PATH}", json={"inputs": batch})
            res.raise_for_status()
            body = res.json()
            if not isinstance(body, list) or len(body) != len(batch):
                raise RuntimeError(
                    f"embedder returned malformed response (expected {len(batch)} vectors)"
                )
            out.extend(body)
    return out


# ---------------------------------------------------------------------------
# DB upsert — per-source full-rebuild so re-ingestion stays in lock-step.
# ---------------------------------------------------------------------------
def upsert_chunks(
    conn: Any,
    *,
    namespace: str,
    source: str,
    chunks: list[Chunk],
    embeddings: list[list[float]],
    actor: str,
) -> dict[str, int]:
    """Delete every chunk previously written for (namespace, source)
    and replace with the new set. Idempotent for the file as a whole:
    re-running with the same input is a no-op (same IDs, same
    content), re-running after an edit replaces the affected rows.

    Records the change in `chunk_audit` so the team can answer
    "which version of the FAQ did the SLM cite on Monday?".

    Returns a `{deleted, inserted}` counter for the caller to log.
    """
    assert len(chunks) == len(embeddings), "chunks/embeddings length mismatch"

    deleted = 0
    inserted = 0
    with conn.cursor() as cur:
        # Capture pre-image rows for audit before deleting.
        cur.execute(
            """
            SELECT id, namespace, content, metadata
              FROM chunks
             WHERE namespace = %s AND metadata->>'source' = %s
            """,
            (namespace, source),
        )
        existing = cur.fetchall()
        for row in existing:
            _audit(cur, row[0], namespace, "deleted",
                   snapshot={"id": row[0], "namespace": row[1], "content": row[2], "metadata": row[3]},
                   actor=actor)
            deleted += 1

        cur.execute(
            "DELETE FROM chunks WHERE namespace = %s AND metadata->>'source' = %s",
            (namespace, source),
        )

        for chunk, vec in zip(chunks, embeddings):
            cur.execute(
                """
                INSERT INTO chunks (id, namespace, content, embedding, metadata)
                VALUES (%s, %s, %s, %s::vector, %s::jsonb)
                """,
                (
                    chunk.id,
                    chunk.namespace,
                    chunk.content,
                    _vector_literal(vec),
                    json.dumps(chunk.metadata),
                ),
            )
            _audit(
                cur,
                chunk.id,
                namespace,
                "inserted",
                snapshot={
                    "id": chunk.id,
                    "namespace": chunk.namespace,
                    "content": chunk.content,
                    "metadata": chunk.metadata,
                },
                actor=actor,
            )
            inserted += 1

    conn.commit()
    return {"deleted": deleted, "inserted": inserted}


def _audit(cur: Any, chunk_id_: str, namespace: str, action: str, *, snapshot: dict, actor: str) -> None:
    cur.execute(
        """
        INSERT INTO chunk_audit (chunk_id, namespace, action, snapshot, actor)
        VALUES (%s, %s, %s, %s::jsonb, %s)
        """,
        (chunk_id_, namespace, action, json.dumps(snapshot, default=str), actor),
    )


def _vector_literal(vec: list[float]) -> str:
    return "[" + ",".join(f"{x:.6g}" for x in vec) + "]"


# ---------------------------------------------------------------------------
# Iteration helpers.
# ---------------------------------------------------------------------------
def iter_markdown_files(root: Path) -> Iterable[Path]:
    """Yield every `.md` file under `root` in stable order so two
    ingestion runs visit the same files in the same order (matters
    for reproducible audit logs).
    """
    if not root.exists():
        return iter(())
    return sorted(p for p in root.rglob("*.md") if p.is_file())


__all__ = [
    "Chunk",
    "Doc",
    "EMBED_BATCH",
    "EMBED_PATH",
    "TARGET_WORDS_PER_CHUNK",
    "chunk_doc",
    "chunk_id",
    "embed_chunks",
    "iter_markdown_files",
    "load_doc",
    "parse_frontmatter",
    "upsert_chunks",
]
