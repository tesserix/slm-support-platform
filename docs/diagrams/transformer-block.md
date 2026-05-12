# Diagram — Transformer Block (the thing we're building in Phase 1)

Standalone reference for the decoder-only transformer block we'll implement in `phase1-from-scratch/05_block.py`.

## One transformer block (pre-norm layout)

```mermaid
graph TB
    X["x (residual stream)<br/>shape: (batch, seq, d_model)"]
    LN1["LayerNorm"]
    ATT["Multi-head self-attention<br/>causal mask<br/>n_heads × d_head"]
    ADD1(("+"))
    LN2["LayerNorm"]
    FFN["FFN<br/>Linear(d_model → 4·d_model)<br/>GELU<br/>Linear(4·d_model → d_model)"]
    ADD2(("+"))
    OUT["x' (next residual stream)"]

    X --> LN1 --> ATT --> ADD1
    X -.skip.-> ADD1
    ADD1 --> LN2 --> FFN --> ADD2
    ADD1 -.skip.-> ADD2
    ADD2 --> OUT
```

## Multi-head attention internals

```mermaid
graph LR
    X["x<br/>(B, T, d_model)"]
    QPROJ["Linear → Q<br/>(B, T, d_model)"]
    KPROJ["Linear → K<br/>(B, T, d_model)"]
    VPROJ["Linear → V<br/>(B, T, d_model)"]
    SPLIT["Reshape into<br/>n_heads × d_head"]
    SCORE["Q · Kᵀ / √d_head"]
    MASK["Causal mask<br/>(upper-tri = -inf)"]
    SM["softmax"]
    WV["× V"]
    MERGE["Concat heads<br/>(B, T, d_model)"]
    OPROJ["Output Linear<br/>(B, T, d_model)"]

    X --> QPROJ --> SPLIT
    X --> KPROJ --> SPLIT
    X --> VPROJ --> SPLIT
    SPLIT --> SCORE --> MASK --> SM --> WV --> MERGE --> OPROJ
```

## Full model = stack of N blocks

```mermaid
graph TB
    TOK["Token IDs<br/>(B, T)"]
    EMB["Token embed<br/>+ pos embed"]
    B1["Block 1"]
    B2["Block 2"]
    BD["…"]
    BN["Block N"]
    LNF["Final LayerNorm"]
    HEAD["LM Head<br/>Linear(d_model → vocab_size)"]
    LOGITS["Logits (B, T, vocab)"]

    TOK --> EMB --> B1 --> B2 --> BD --> BN --> LNF --> HEAD --> LOGITS
```

## Key dimensions to remember

For our Phase 1 target (~25M params on TinyStories):

| Hyperparam | Value | Why |
|-----------|-------|-----|
| `vocab_size` | ~2,000–10,000 | TinyStories has a small vocabulary; smaller vocab = smaller embedding matrix |
| `d_model` | 256 | Width of the residual stream |
| `n_layers` | 6 | Depth |
| `n_heads` | 8 | `d_head = d_model / n_heads = 32` |
| `seq_len` | 256 | Max context window — TinyStories are short, no need for 2048+ |
| `ffn_hidden` | 4 × d_model = 1024 | Standard ratio |

Multiply it through: ~25M params, fits comfortably on a laptop GPU, trains in a couple hours.
