"""Step 4 — Scaled dot-product attention + causal multi-head attention.

The single most important module in the model. Everything else (token
embedding, FFN, residual connections) is plumbing; attention is the
algorithm that decides which prior tokens influence the next one.

The math, with no PyTorch:

    Q, K, V = x @ W_q, x @ W_k, x @ W_v
    scores  = Q @ K.T / sqrt(d_head)        # (T, T) — every-pair affinity
    scores += causal_mask                    # set upper-triangle to -inf
    weights = softmax(scores, dim=-1)        # row-normalised
    out     = weights @ V                    # weighted average of values

Multi-head is just running that math `n_heads` times in parallel with
different W_q / W_k / W_v projections, then concatenating the results.

This file:
  - `scaled_dot_product_attention`: a tensor-only function, batched over
    (B, H, T). No state. Easy to read.
  - `MultiHeadCausalAttention`: an `nn.Module` wrapping the function
    with the four learned linear projections (q, k, v, output).

Run directly for shape + causality checks:

    python phase1-from-scratch/04_attention.py
"""

from __future__ import annotations

import math
import sys
from pathlib import Path

import torch
from torch import nn

_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from config import MODEL  # noqa: E402


def scaled_dot_product_attention(
    q: torch.Tensor,
    k: torch.Tensor,
    v: torch.Tensor,
    mask: torch.Tensor | None = None,
) -> torch.Tensor:
    """Compute attention(q, k, v).

    Shapes:
        q, k, v: (..., T, d_head)
        mask:    (..., T, T) — True (or 0) where the position is ALLOWED;
                              False (or -inf) where it must be hidden.
                              We pass an additive mask: zeros for allowed,
                              -inf for hidden. Caller builds whichever is
                              easier (we build causal below).
    Returns:
        (..., T, d_head)

    All leading dims are batched freely — typical caller passes (B, H, T, D).
    """
    d_head = q.size(-1)
    # (..., T, T)
    scores = (q @ k.transpose(-2, -1)) / math.sqrt(d_head)
    if mask is not None:
        scores = scores + mask
    weights = torch.softmax(scores, dim=-1)
    # (..., T, D)
    return weights @ v


def build_causal_mask(seq_len: int, device: torch.device | None = None) -> torch.Tensor:
    """An additive (T, T) mask: 0 below+on the diagonal, -inf above.

    Adding this to the raw scores before softmax forces the model to
    only attend to positions <= the current one. That's the entire
    reason this is a DECODER-only model: no peeking at future tokens.
    """
    mask = torch.zeros(seq_len, seq_len, device=device)
    mask = mask.masked_fill(
        torch.triu(torch.ones(seq_len, seq_len, dtype=torch.bool, device=device), diagonal=1),
        float("-inf"),
    )
    return mask


class MultiHeadCausalAttention(nn.Module):
    """`n_heads` parallel attention heads with one residual-stream-wide
    linear projection at the output.

    Per the original Transformer paper: the head dimension is
        d_head = d_model // n_heads
    so the concatenated multi-head output is also d_model wide. That's
    why the output projection W_o is also (d_model, d_model).
    """

    def __init__(self, d_model: int, n_heads: int, dropout: float = 0.0):
        super().__init__()
        if d_model % n_heads != 0:
            raise ValueError(f"d_model {d_model} must be divisible by n_heads {n_heads}")
        self.d_model = d_model
        self.n_heads = n_heads
        self.d_head = d_model // n_heads

        # One Linear for each of Q, K, V — kept separate for clarity.
        # Many implementations fuse into a single (d_model, 3*d_model)
        # Linear for a small perf win; the math is identical.
        self.W_q = nn.Linear(d_model, d_model, bias=False)
        self.W_k = nn.Linear(d_model, d_model, bias=False)
        self.W_v = nn.Linear(d_model, d_model, bias=False)
        self.W_o = nn.Linear(d_model, d_model, bias=False)

        self.attn_dropout = nn.Dropout(dropout)
        self.resid_dropout = nn.Dropout(dropout)

        # Pre-compute the causal mask for the max seq_len; we'll slice
        # it down per forward to the actual T we got. Stored as a
        # buffer so it follows the module to GPU but doesn't appear
        # in state_dict (persistent=False).
        self.register_buffer(
            "causal_mask",
            build_causal_mask(MODEL.seq_len),
            persistent=False,
        )

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        # x: (B, T, d_model)
        B, T, _ = x.shape

        # Project to Q, K, V. Shape: (B, T, d_model)
        q = self.W_q(x)
        k = self.W_k(x)
        v = self.W_v(x)

        # Split into heads: (B, T, n_heads, d_head) -> (B, n_heads, T, d_head)
        def _split(t: torch.Tensor) -> torch.Tensor:
            return t.view(B, T, self.n_heads, self.d_head).transpose(1, 2)
        q, k, v = _split(q), _split(k), _split(v)

        # Slice the cached causal mask to (T, T) and broadcast to
        # (B, n_heads, T, T). softmax handles the -inf rows fine.
        mask = self.causal_mask[:T, :T]

        # (B, n_heads, T, d_head)
        attn_out = scaled_dot_product_attention(q, k, v, mask=mask)
        attn_out = self.attn_dropout(attn_out)

        # Re-merge heads: (B, T, n_heads, d_head) -> (B, T, d_model)
        attn_out = attn_out.transpose(1, 2).contiguous().view(B, T, self.d_model)

        # Final output projection + dropout. This is the LINEAR layer
        # the model can use to weight which head's information is most
        # useful for any given residual-stream feature.
        return self.resid_dropout(self.W_o(attn_out))


def _smoke_test() -> None:
    print("=" * 64)
    print("smoke test — attention shapes and causality check")
    print("=" * 64)
    torch.manual_seed(0)

    B, T = 2, 16
    attn = MultiHeadCausalAttention(d_model=MODEL.d_model, n_heads=MODEL.n_heads)
    x = torch.randn(B, T, MODEL.d_model)
    y = attn(x)

    print(f"  input:  {tuple(x.shape)}")
    print(f"  output: {tuple(y.shape)}")
    assert y.shape == x.shape

    # Causality property: changing tokens at position t should NOT change
    # the output at positions < t. We check this by perturbing the last
    # token and verifying earlier outputs are unchanged.
    x2 = x.clone()
    x2[:, -1, :] += 100.0
    with torch.no_grad():
        y_alt = attn(x2)
    diff = (y - y_alt).abs()
    early_diff = diff[:, :-1, :].max().item()
    last_diff = diff[:, -1, :].max().item()
    print(f"  perturbing last token →")
    print(f"    max change at earlier positions: {early_diff:.2e}  (should be ~0)")
    print(f"    max change at last position:     {last_diff:.4f}   (should be > 0)")
    assert early_diff < 1e-5, "causal mask is leaking future info"
    assert last_diff > 1e-3, "attention is somehow ignoring its inputs"
    print("  causality preserved ✓")

    n_params = sum(p.numel() for p in attn.parameters())
    print(f"\n  attention params: {n_params:,}")
    print(f"    = 4 × (d_model × d_model) — Q, K, V, O projections")


if __name__ == "__main__":
    _smoke_test()
