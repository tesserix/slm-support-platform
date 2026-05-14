"""Tests for the docs-corpus ingestion pipeline.

The DB and embedder side are mocked — they're well-defined external
contracts and integration-testing them would need a live pgvector +
embedder, which CI doesn't have. The tests instead pin down the
behaviour we care about: frontmatter parsing handles every shape the
seed docs actually use, chunking respects target word counts without
shattering paragraphs, IDs are deterministic, and the upsert flow
deletes-then-inserts in the right order so an interrupted run doesn't
leave duplicates behind.
"""
from __future__ import annotations

import json
from pathlib import Path
from unittest.mock import MagicMock, call

import httpx
import pytest
import respx

from mcp_gateway import ingest


# -----------------------------------------------------------------------------
# Frontmatter parsing — has to be resilient because the seed docs are
# hand-authored and someone will eventually mistype something.
# -----------------------------------------------------------------------------
def test_parse_frontmatter_empty_doc() -> None:
    meta, body = ingest.parse_frontmatter("Just markdown, no frontmatter.\n")
    assert meta == {}
    assert body == "Just markdown, no frontmatter.\n"


def test_parse_frontmatter_string_values() -> None:
    raw = (
        "---\n"
        "title: How points work\n"
        "audience: customer\n"
        "---\n"
        "\n"
        "body content\n"
    )
    meta, body = ingest.parse_frontmatter(raw)
    assert meta == {"title": "How points work", "audience": "customer"}
    # The closing `---\n` consumes trailing whitespace, so the body
    # the caller sees starts at the first non-blank line.
    assert body == "body content\n"


def test_parse_frontmatter_list_shorthand() -> None:
    """The seed docs use `tags: [a, b, c]`. Make sure that becomes a
    list of strings, not the literal string '[a, b, c]'."""
    raw = "---\ntags: [points, scoring, predictions]\n---\nbody\n"
    meta, _ = ingest.parse_frontmatter(raw)
    assert meta["tags"] == ["points", "scoring", "predictions"]


def test_parse_frontmatter_strips_quotes() -> None:
    raw = "---\ntitle: 'quoted title'\nother: \"also quoted\"\n---\nbody\n"
    meta, _ = ingest.parse_frontmatter(raw)
    assert meta["title"] == "quoted title"
    assert meta["other"] == "also quoted"


def test_parse_frontmatter_ignores_comments_and_blanks() -> None:
    raw = "---\n# this is a comment\n\ntitle: ok\n---\nbody\n"
    meta, _ = ingest.parse_frontmatter(raw)
    assert meta == {"title": "ok"}


def test_parse_frontmatter_handles_malformed_line_gracefully() -> None:
    """A line without a colon shouldn't crash — just skip it. Future
    authors making a typo deserve a degraded read, not a startup
    failure."""
    raw = "---\nbroken-line-no-colon\ntitle: ok\n---\nbody\n"
    meta, _ = ingest.parse_frontmatter(raw)
    assert meta == {"title": "ok"}


# -----------------------------------------------------------------------------
# Loading + title derivation.
# -----------------------------------------------------------------------------
def test_load_doc_uses_frontmatter_title(tmp_path: Path) -> None:
    doc_path = tmp_path / "foo.md"
    doc_path.write_text("---\ntitle: From frontmatter\n---\n# Heading shouldn't win\nbody\n")
    doc = ingest.load_doc(doc_path, root=tmp_path)
    assert doc.title == "From frontmatter"
    assert doc.source == "foo.md"


def test_load_doc_falls_back_to_first_heading(tmp_path: Path) -> None:
    doc_path = tmp_path / "foo.md"
    doc_path.write_text("# The actual title\n\nBody content here.\n")
    doc = ingest.load_doc(doc_path, root=tmp_path)
    assert doc.title == "The actual title"


def test_load_doc_falls_back_to_filename_stem(tmp_path: Path) -> None:
    """If frontmatter has no title AND there's no leading heading,
    use the filename. Almost always still useful."""
    doc_path = tmp_path / "battle-rooms.md"
    doc_path.write_text("Body with no heading at all.\n")
    doc = ingest.load_doc(doc_path, root=tmp_path)
    assert doc.title == "battle-rooms"


def test_load_doc_source_is_relative_to_root(tmp_path: Path) -> None:
    subdir = tmp_path / "fanzone"
    subdir.mkdir()
    doc_path = subdir / "x.md"
    doc_path.write_text("body\n")
    doc = ingest.load_doc(doc_path, root=tmp_path)
    assert doc.source == "fanzone/x.md"


# -----------------------------------------------------------------------------
# Chunking — paragraph packing.
# -----------------------------------------------------------------------------
def _make_doc(body: str) -> ingest.Doc:
    return ingest.Doc(source="x.md", title="t", body=body, metadata={})


def test_chunk_empty_body_yields_no_chunks() -> None:
    assert ingest.chunk_doc(_make_doc(""), namespace="t") == []
    assert ingest.chunk_doc(_make_doc("   \n  "), namespace="t") == []


def test_chunk_single_short_doc_emits_one_chunk() -> None:
    chunks = ingest.chunk_doc(_make_doc("Short doc body."), namespace="t")
    assert len(chunks) == 1
    assert chunks[0].content == "Short doc body."


def test_chunk_respects_target_word_count() -> None:
    """Three big paragraphs (>200 words each) should produce three
    chunks — we don't pack across the target if we'd massively
    overshoot."""
    big = " ".join(["word"] * 250)
    body = f"{big}\n\n{big}\n\n{big}"
    chunks = ingest.chunk_doc(_make_doc(body), namespace="t")
    assert len(chunks) == 3


def test_chunk_packs_small_paragraphs_together() -> None:
    """Lots of one-line paragraphs should be packed into a single
    chunk, not yield 20 stub-sized chunks."""
    body = "\n\n".join("one liner number {}".format(i) for i in range(20))
    chunks = ingest.chunk_doc(_make_doc(body), namespace="t")
    assert len(chunks) == 1


def test_chunk_keeps_code_fences_intact() -> None:
    """A code fence must never be split across chunks — the SLM relies
    on the whole snippet to reason about it."""
    body = (
        "Intro paragraph.\n\n"
        "```python\n"
        + "line\n" * 50
        + "```\n\n"
        "Outro paragraph."
    )
    chunks = ingest.chunk_doc(_make_doc(body), namespace="t")
    # Every chunk that contains a fence open must also contain the
    # matching close — count them.
    for chunk in chunks:
        opens = chunk.content.count("```")
        assert opens % 2 == 0, f"unbalanced code fence in chunk: {chunk.content!r}"


def test_chunk_id_is_deterministic() -> None:
    a = ingest.chunk_id("t", "doc.md", 0)
    b = ingest.chunk_id("t", "doc.md", 0)
    assert a == b


def test_chunk_id_changes_with_index_namespace_source() -> None:
    base = ingest.chunk_id("t", "doc.md", 0)
    assert base != ingest.chunk_id("t", "doc.md", 1)
    assert base != ingest.chunk_id("t2", "doc.md", 0)
    assert base != ingest.chunk_id("t", "other.md", 0)


def test_chunk_carries_source_metadata() -> None:
    """The retrieval side renders `metadata.source` as a citation, so
    every chunk needs to carry the source path through."""
    doc = ingest.Doc(
        source="fanzone/points.md",
        title="Points",
        body="Para one.\n\nPara two.",
        metadata={"tags": ["points"]},
    )
    chunks = ingest.chunk_doc(doc, namespace="fanzone")
    for c in chunks:
        assert c.metadata["source"] == "fanzone/points.md"
        assert c.metadata["title"] == "Points"
        assert c.metadata["tags"] == ["points"]
        assert "chunk_index" in c.metadata
        assert "chunk_count" in c.metadata


# -----------------------------------------------------------------------------
# Embedding — batching + error surfacing.
# -----------------------------------------------------------------------------
@respx.mock
async def test_embed_chunks_batches_when_over_limit() -> None:
    """50 chunks > EMBED_BATCH (32) so we expect two POSTs."""
    route = respx.post("https://embed.test/embed").mock(
        return_value=httpx.Response(200, json=[[0.1] * 384] * 32)
    )
    # Second batch returns 18.
    route_2 = respx.post("https://embed.test/embed").mock(
        return_value=httpx.Response(200, json=[[0.2] * 384] * 18)
    )
    # respx serves routes in registration order, last-mocked wins for
    # the same matcher — so we manually thread responses below.
    queue = [
        httpx.Response(200, json=[[0.1] * 384] * 32),
        httpx.Response(200, json=[[0.2] * 384] * 18),
    ]
    respx.post("https://embed.test/embed").mock(side_effect=lambda req: queue.pop(0))

    contents = [f"chunk {i}" for i in range(50)]
    out = await ingest.embed_chunks("https://embed.test", contents)
    assert len(out) == 50


@respx.mock
async def test_embed_chunks_raises_on_count_mismatch() -> None:
    """A malformed embedder response should fail loudly, not silently
    produce fewer chunks than we asked for."""
    respx.post("https://embed.test/embed").mock(
        return_value=httpx.Response(200, json=[[0.1] * 384])  # 1 vector for 2 chunks
    )
    with pytest.raises(RuntimeError, match="malformed response"):
        await ingest.embed_chunks("https://embed.test", ["a", "b"])


# -----------------------------------------------------------------------------
# DB upsert — delete-then-insert, with audit rows.
# -----------------------------------------------------------------------------
def _fake_conn():
    """Build a MagicMock that mimics psycopg's context-manager surface
    just enough for upsert_chunks. cur.fetchall() returns whatever
    we queued; cur.execute records every SQL call for assertions."""
    cur = MagicMock()
    cur.fetchall.return_value = []
    cm_cur = MagicMock()
    cm_cur.__enter__ = MagicMock(return_value=cur)
    cm_cur.__exit__ = MagicMock(return_value=False)
    conn = MagicMock()
    conn.cursor.return_value = cm_cur
    return conn, cur


def test_upsert_inserts_when_no_existing_rows() -> None:
    conn, cur = _fake_conn()
    chunks = [
        ingest.Chunk(id="abc", namespace="t", content="hello", metadata={"source": "x.md"}),
        ingest.Chunk(id="def", namespace="t", content="world", metadata={"source": "x.md"}),
    ]
    embeddings = [[0.1] * 384, [0.2] * 384]
    counts = ingest.upsert_chunks(
        conn, namespace="t", source="x.md", chunks=chunks, embeddings=embeddings, actor="test"
    )
    assert counts == {"deleted": 0, "inserted": 2}
    # Validate the delete happened before any insert. Hunt for the DELETE call.
    sqls = [args[0][0] for args in cur.execute.call_args_list]
    delete_idx = next(i for i, s in enumerate(sqls) if s.strip().startswith("DELETE"))
    first_insert_idx = next(i for i, s in enumerate(sqls) if "INSERT INTO chunks" in s)
    assert delete_idx < first_insert_idx
    conn.commit.assert_called_once()


def test_upsert_audits_deletions_before_replacing() -> None:
    conn, cur = _fake_conn()
    cur.fetchall.return_value = [
        ("old_id", "t", "old content", {"source": "x.md"}),
    ]
    chunks = [
        ingest.Chunk(id="new_id", namespace="t", content="new content", metadata={"source": "x.md"}),
    ]
    counts = ingest.upsert_chunks(
        conn,
        namespace="t",
        source="x.md",
        chunks=chunks,
        embeddings=[[0.0] * 384],
        actor="test",
    )
    assert counts == {"deleted": 1, "inserted": 1}
    # The audit insert for the deletion must appear before the chunk
    # delete — otherwise we'd be auditing a snapshot we'd already lost.
    sqls = [args[0][0] for args in cur.execute.call_args_list]
    deleted_audit_idx = next(
        i for i, s in enumerate(sqls)
        if "INSERT INTO chunk_audit" in s
    )
    chunk_delete_idx = next(
        i for i, s in enumerate(sqls)
        if s.strip().startswith("DELETE FROM chunks")
    )
    assert deleted_audit_idx < chunk_delete_idx


def test_upsert_only_touches_matching_source() -> None:
    """An upsert for `points.md` must NEVER delete chunks belonging
    to `battle-rooms.md` — the namespace alone isn't enough scoping."""
    conn, cur = _fake_conn()
    cur.fetchall.return_value = []
    ingest.upsert_chunks(
        conn,
        namespace="fanzone",
        source="points.md",
        chunks=[ingest.Chunk(id="i", namespace="fanzone", content="x", metadata={"source": "points.md"})],
        embeddings=[[0.0] * 384],
        actor="test",
    )
    delete_call = next(
        c for c in cur.execute.call_args_list
        if c[0][0].strip().startswith("DELETE FROM chunks")
    )
    sql, params = delete_call[0][0], delete_call[0][1]
    assert params == ("fanzone", "points.md")
    # Make sure the WHERE clause references both columns.
    assert "namespace" in sql and "source" in sql


def test_upsert_metadata_is_json_serialised() -> None:
    """Postgres' jsonb column needs a string at insert time, not a
    Python dict. Make sure the wire-format conversion happens."""
    conn, cur = _fake_conn()
    chunk = ingest.Chunk(
        id="i",
        namespace="t",
        content="x",
        metadata={"source": "x.md", "tags": ["a", "b"]},
    )
    ingest.upsert_chunks(
        conn,
        namespace="t",
        source="x.md",
        chunks=[chunk],
        embeddings=[[0.0] * 384],
        actor="test",
    )
    insert_call = next(
        c for c in cur.execute.call_args_list
        if "INSERT INTO chunks" in c[0][0]
    )
    params = insert_call[0][1]
    metadata_param = params[-1]
    assert isinstance(metadata_param, str)
    assert json.loads(metadata_param) == chunk.metadata


# -----------------------------------------------------------------------------
# File discovery.
# -----------------------------------------------------------------------------
def test_iter_markdown_files_returns_stable_order(tmp_path: Path) -> None:
    for name in ["c.md", "a.md", "b.md", "skip.txt"]:
        (tmp_path / name).write_text("body")
    found = [p.name for p in ingest.iter_markdown_files(tmp_path)]
    assert found == ["a.md", "b.md", "c.md"]


def test_iter_markdown_files_recurses(tmp_path: Path) -> None:
    (tmp_path / "deep" / "subdir").mkdir(parents=True)
    (tmp_path / "deep" / "subdir" / "x.md").write_text("body")
    found = [p.name for p in ingest.iter_markdown_files(tmp_path)]
    assert "x.md" in found


def test_iter_markdown_files_missing_dir_returns_empty(tmp_path: Path) -> None:
    found = list(ingest.iter_markdown_files(tmp_path / "does-not-exist"))
    assert found == []
