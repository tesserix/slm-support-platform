# Diagram — Transformer Block (the thing we're building in Phase 1)

Standalone reference for the decoder-only transformer block we'll implement in `phase1-from-scratch/05_block.py`.

## One transformer block (pre-norm layout)

```mermaid
graph TB
    X[x residual stream]
    LN1[LayerNorm]
    ATT[Multi head self attention causal mask]
    ADD1[plus]
    LN2[LayerNorm]
    FFN[FFN Linear GELU Linear]
    ADD2[plus]
    OUT[x next residual stream]

    X --> LN1
    LN1 --> ATT
    ATT --> ADD1
    X --> ADD1
    ADD1 --> LN2
    LN2 --> FFN
    FFN --> ADD2
    ADD1 --> ADD2
    ADD2 --> OUT
```

Two skip connections feed back into the residual stream: the input `x` bypasses the attention sub-layer and adds in at `ADD1`, then the post-attention value bypasses the FFN sub-layer and adds in at `ADD2`. That bypass is the residual stream — every block adds, never replaces.

## Multi-head attention internals

```mermaid
graph LR
    X[x input]
    QPROJ[Linear to Q]
    KPROJ[Linear to K]
    VPROJ[Linear to V]
    SPLIT[Reshape into n heads]
    SCORE[Q dot K transpose scaled]
    MASK[Causal mask upper triangle to negative infinity]
    SM[softmax]
    WV[multiply by V]
    MERGE[Concat heads]
    OPROJ[Output Linear]

    X --> QPROJ
    X --> KPROJ
    X --> VPROJ
    QPROJ --> SPLIT
    KPROJ --> SPLIT
    VPROJ --> SPLIT
    SPLIT --> SCORE
    SCORE --> MASK
    MASK --> SM
    SM --> WV
    WV --> MERGE
    MERGE --> OPROJ
```

## Full model = stack of N blocks

```mermaid
graph TB
    TOK[Token IDs]
    EMB[Token plus positional embedding]
    B1[Block 1]
    B2[Block 2]
    BD[more blocks]
    BN[Block N]
    LNF[Final LayerNorm]
    HEAD[LM Head Linear to vocab size]
    LOGITS[Logits]

    TOK --> EMB
    EMB --> B1
    B1 --> B2
    B2 --> BD
    BD --> BN
    BN --> LNF
    LNF --> HEAD
    HEAD --> LOGITS
```

## Key dimensions to remember

For our Phase 1 target (~25M params on TinyStories):

| Hyperparam | Value | Why |
|-----------|-------|-----|
| `vocab_size` | ~2,000 to 10,000 | TinyStories has a small vocabulary; smaller vocab = smaller embedding matrix |
| `d_model` | 256 | Width of the residual stream |
| `n_layers` | 6 | Depth |
| `n_heads` | 8 | `d_head = d_model / n_heads = 32` |
| `seq_len` | 256 | Max context window — TinyStories are short, no need for 2048+ |
| `ffn_hidden` | 4 * d_model = 1024 | Standard ratio |

Multiply it through: ~25M params, fits comfortably on a laptop GPU, trains in a couple hours.
