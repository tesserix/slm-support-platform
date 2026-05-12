"""Step 3 — Token + positional embeddings.

Two tiny `nn.Module`s that together turn a sequence of token-id integers
into the dense vectors the transformer block consumes.

  TokenEmbedding:       (B, T) int ids        → (B, T, d_model) floats
  LearnedPositionalEmbedding: also (B, T) → (B, T, d_model) but encoding
                              POSITION, not identity

A decoder-only transformer sums the two and feeds the result into the
first block. Without the positional component the model would see
{"the","cat","sat"} and {"cat","the","sat"} as identical inputs —
attention is permutation-invariant on its own.

We use LEARNED absolute positional embeddings here (a second
`nn.Embedding`) because they're the simplest to read. Real production
SLMs almost all use RoPE (rotary position embedding) — that's a
worthwhile follow-up exercise but not on the critical path for
understanding the rest of the stack.

Run directly for a shape check:

    python phase1-from-scratch/03_embeddings.py
"""

from __future__ import annotations

import sys
from pathlib import Path

import torch
from torch import nn

_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from config import MODEL  # noqa: E402


class TokenEmbedding(nn.Module):
    """A learned `vocab_size × d_model` lookup table.

    nn.Embedding is just a thin wrapper over a Parameter matrix — the
    "embedding" is literally the row of the weight matrix at the index
    of the input id. Initialised normal(0, 0.02) per the GPT-2 default.
    """

    def __init__(self, vocab_size: int, d_model: int):
        super().__init__()
        self.weight = nn.Parameter(torch.empty(vocab_size, d_model))
        nn.init.normal_(self.weight, mean=0.0, std=0.02)
        self.vocab_size = vocab_size
        self.d_model = d_model

    def forward(self, token_ids: torch.Tensor) -> torch.Tensor:
        # token_ids: (B, T) int64 in [0, vocab_size)
        # returns:   (B, T, d_model)
        return self.weight[token_ids]


class LearnedPositionalEmbedding(nn.Module):
    """A learned `seq_len × d_model` table indexed by position 0..T-1.

    Each forward pass slices the first T rows. Same init as
    TokenEmbedding so the two are on the same scale when summed.
    """

    def __init__(self, seq_len: int, d_model: int):
        super().__init__()
        self.weight = nn.Parameter(torch.empty(seq_len, d_model))
        nn.init.normal_(self.weight, mean=0.0, std=0.02)
        self.seq_len = seq_len
        self.d_model = d_model

    def forward(self, T: int) -> torch.Tensor:
        # returns (T, d_model); the caller broadcasts to (B, T, d_model)
        if T > self.seq_len:
            raise ValueError(
                f"sequence length {T} > max positional embedding {self.seq_len}; "
                "increase MODEL.seq_len in config.py or truncate the input"
            )
        return self.weight[:T]


class InputEmbedding(nn.Module):
    """Composes TokenEmbedding + LearnedPositionalEmbedding.

    Lives here as a single module so the model code in step 6 has one
    clean input layer to reference. The two sub-modules are exposed as
    attributes so a test or training loop can inspect / regularise them
    independently.
    """

    def __init__(self, vocab_size: int, seq_len: int, d_model: int):
        super().__init__()
        self.token = TokenEmbedding(vocab_size, d_model)
        self.position = LearnedPositionalEmbedding(seq_len, d_model)

    def forward(self, token_ids: torch.Tensor) -> torch.Tensor:
        # token_ids: (B, T)
        B, T = token_ids.shape
        tok = self.token(token_ids)              # (B, T, d_model)
        pos = self.position(T).unsqueeze(0)      # (1, T, d_model)
        return tok + pos                         # (B, T, d_model)


def _smoke_test() -> None:
    print("=" * 60)
    print("smoke test — InputEmbedding shapes")
    print("=" * 60)
    torch.manual_seed(0)
    emb = InputEmbedding(
        vocab_size=MODEL.vocab_size,
        seq_len=MODEL.seq_len,
        d_model=MODEL.d_model,
    )
    x = torch.randint(0, MODEL.vocab_size, (2, 16))   # (B=2, T=16)
    y = emb(x)
    print(f"  input ids:        {tuple(x.shape)}  dtype={x.dtype}")
    print(f"  embedding output: {tuple(y.shape)}  dtype={y.dtype}")
    assert y.shape == (2, 16, MODEL.d_model)
    print(f"  output norm[0,0]: {y[0, 0].norm().item():.4f}")

    n_params = sum(p.numel() for p in emb.parameters())
    print(f"  total params: {n_params:,}")
    print(f"    of which token: {emb.token.weight.numel():,}")
    print(f"    of which pos:   {emb.position.weight.numel():,}")
    print("  (token embedding dominates at this vocab size — that's normal.)")


if __name__ == "__main__":
    _smoke_test()
