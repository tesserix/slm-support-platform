# Diagrams

Two architecture diagrams in `.drawio` format. They open in **diagrams.net** (free, browser or desktop), **Lucidchart** (File → Import), the **VS Code Draw.io Integration** extension, or any tool that speaks the drawio XML format.

## Files

| File | What it shows |
|------|---------------|
| [`architecture.drawio`](architecture.drawio) | System architecture: all five products → chat widgets → edge → support platform (BFF, Router, Orchestrator, MCP, RAG, SLM) → per-product MCP servers → product APIs |
| [`customer-flow.drawio`](customer-flow.drawio) | Customer request flow: swim-lane view of one chat message travelling through every component, end to end |
| `end-state.md` | Mermaid version of the architecture, embedded in markdown for inline reading |
| `transformer-block.md` | Mermaid diagrams of the Phase 1 transformer block (model architecture, not platform) |

## How to open

### Option 1 — diagrams.net (browser, no install)

1. Go to https://app.diagrams.net
2. **Open Existing Diagram** → **Device** → pick the `.drawio` file
3. Edit, save back to disk, commit

### Option 2 — VS Code

1. Install the **Draw.io Integration** extension (`hediet.vscode-drawio`)
2. Open the `.drawio` file directly — it renders inline as an editable canvas
3. Save (Cmd+S) writes the XML back to the file

### Option 3 — Lucidchart

1. Lucidchart → **File** → **Import Diagram**
2. Pick **Draw.io** as the format
3. Upload the `.drawio` file
4. Edit in Lucidchart (changes won't sync back to git unless you re-export)

### Option 4 — Desktop drawio app

Download from https://github.com/jgraph/drawio-desktop/releases and open the file directly.

## What's where in `architecture.drawio`

```
CUSTOMERS (green band)
    ↓
PRODUCTS (5 blue cards: mark8ly, fanzone, HomeChef, gameverse, stockpilot)
    ↓ each embeds a Chat widget (orange)
EDGE (Cloudflare → Istio Gateway)
    ↓
SUPPORT PLATFORM (purple container, GKE namespace: support-platform)
    support-bff
        ↓
    Router Agent → Agent Orchestrator
        ↓               ↓               ↓
    MCP LAYER       RAG LAYER       SLM LAYER
    (cyan)          (amber)         (red)
        ↓
MCP SERVERS (cyan row: one per product, owns tool surface for that product)
        ↓
PRODUCT APIs (light-blue row: existing per-product backends)
```

## What's where in `customer-flow.drawio`

Eight vertical swim lanes, left to right:

1. **Customer**
2. **Chat Widget**
3. **support-bff**
4. **Router + Orchestrator**
5. **RAG + Vector DB**
6. **SLM (vLLM + Phi-3)**
7. **MCP Client**
8. **homechef-mcp + homechef-api** (the per-product MCP server and the API it wraps)

Twelve numbered steps run diagonally down through the lanes, showing the order in which each component participates. The "tool result" and "stream back" return arrows are dashed.

## Editing tips

- **Keep colour coding consistent** with the legend in `architecture.drawio` — products blue, widgets orange, support platform purple, MCP cyan, RAG amber, SLM red, product APIs light blue.
- **Add new products** by duplicating one of the five product cards, the matching chat-widget box, the MCP server box, and the API box — then wire the four together vertically.
- **Add a new tool** by appending to the relevant `*-mcp` server's tool list (the bulleted text inside the cyan box).
