# Per-tenant help-docs corpus

Markdown source for the knowledge base the `search_knowledge_base`
MCP tool retrieves from. One subdirectory per tenant:

```
docs/
├── fanzone/
├── homechef/
├── mark8ly/
├── stockpilot/
├── gameverse/
├── horoscope/
└── scrapper/
```

Every `*.md` under these directories gets ingested into the
`chunks` table with `namespace=<tenant>`. The SLM never sees the raw
markdown — it sees retrieved chunks at query time, with their source
file recorded in chunk metadata so the answer can cite where it came
from.

## File format

Each doc is plain markdown with an optional YAML-ish frontmatter
block. See `fanzone/points-and-scoring.md` for the shape we use:

```markdown
---
title: How points and scoring work in FanZone
tags: [points, scoring, predictions]
audience: customer
---

# How points and scoring work in FanZone

Body content here...
```

- `title` — used as the citation label. Falls back to the first
  `# heading` then the filename if absent.
- `tags` — optional; surfaced in `chunk.metadata.tags` for future
  filtering.
- `audience` — `customer` is the only audience we currently retrieve
  for the customer-facing widget.

## Writing for the SLM

The SLM cites passages it retrieves, so the writing style should be
**self-contained per paragraph**. A reader who lands on paragraph 3
without context should still get a useful answer.

Other rules of thumb:

- Lead with the most-asked thing. Long preambles get truncated when
  the SLM picks a short citation.
- Keep paragraphs under ~200 words. Longer ones get split mid-thought
  during chunking.
- Use code fences for actual code; the chunker treats them as
  indivisible blocks.
- Avoid links to internal admin tools (anything the customer can't
  reach). Internal references confuse the SLM into suggesting flows
  the customer can't follow.
- Talk to the customer in the second person ("your order") rather
  than describing them in the third person ("the customer's order").
- Numbers, not vague hedges: "within 5 minutes" beats "soon".

## Re-ingesting after an edit

After adding or editing a markdown file, run:

```bash
python scripts/ingest_docs.py \
    --tenant fanzone \
    --docs-dir docs/fanzone \
    --vector-db-dsn "$VECTOR_DB_DSN" \
    --embedder-url "$EMBEDDER_URL"
```

The script:

1. Reads every `*.md` under `--docs-dir`.
2. Splits frontmatter, chunks each body into ~200-word windows.
3. Embeds each chunk via the embedder service.
4. Deletes every prior row for `(namespace, source)` and re-inserts.

Re-running is **idempotent** — the same file produces the same
chunks. Edit the markdown, re-run, the index updates.

Use `--dry-run` to skip the embedder + DB writes; the script will
just print how many chunks would be produced per file. Useful for
testing chunking changes.

## Operational rhythm

Today the ingestion script is run manually after a docs change. The
intended future shape is a CronJob in `tesserix-k8s` that polls this
repo's `docs/` directory once an hour and re-runs ingestion if
anything changed. That's not deployed yet — operationalising it is
tracked separately from the Phase-2 work that landed the script.

## I want to add docs for a tenant that isn't in the list

Tenant slugs are pinned in `mcp_gateway/config.py:SUPPORTED_TENANTS`.
Add the slug there, add a directory here, run the ingest, and the
new tenant's mcp-gateway pod will pick up the corpus on its next
`search_knowledge_base` call.
