"""Step 5 — One transformer block (pre-norm layout).

A transformer block is a `f(x) = x + sublayer(LN(x))` pattern applied
twice — once with attention as the sublayer, once with a feed-forward
network. The residual `x +` is the most important thing in the design:
it lets gradients flow through arbitrarily many blocks without vanishing.

The pre-norm layout (LayerNorm BEFORE each sublayer) is the modern
default — it trains more stably than the original post-norm without
needing learning-rate warmup-then-decay gymnastics. Almost every SLM
shipped since GPT-2 uses pre-norm.

  x ─→ LN ─→ attention ─→ + ─→ LN ─→ FFN ─→ + ─→ out
  └────────────skip──────────┘   └────skip─────┘

The FFN is two linears with a GELU non-linearity in between. The
expansion factor is 4× by convention (so `ffn_hidden = 4 * d_model`).
This is a LOT of parameters — roughly 8/12 of every block is FFN, 4/12
is attention. Most of the model's capacity lives here, not in attention.

Run directly for a shape check:

    python phase1-from-scratch/05_block.py
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

# Import attention from the previous step. The file name starts with a
# digit so we have to use __import__ rather than a normal import statement.
_attn_mod = __import__("04_attention")
MultiHeadCausalAttention = _attn_mod.MultiHeadCausalAttention


class FeedForward(nn.Module):
    """Linear → GELU → Linear.

    The expansion factor (typically 4) is what gives the model space to
    do non-linear computation between attention rounds — attention can
    only do linear combinations of the input values, so without an
    FFN the model would be linear end-to-end (modulo softmax).
    """

    def __init__(self, d_model: int, ffn_hidden: int, dropout: float = 0.0):
        super().__init__()
        self.linear_in = nn.Linear(d_model, ffn_hidden, bias=True)
        self.linear_out = nn.Linear(ffn_hidden, d_model, bias=True)
        self.dropout = nn.Dropout(dropout)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        # x: (B, T, d_model)
        h = self.linear_in(x)
        h = torch.nn.functional.gelu(h, approximate="tanh")
        h = self.linear_out(h)
        return self.dropout(h)


class TransformerBlock(nn.Module):
    """One pre-norm transformer block.

    Two LayerNorms + one attention + one FFN + two residual adds.
    """

    def __init__(
        self,
        d_model: int,
        n_heads: int,
        ffn_hidden: int,
        dropout: float = 0.0,
    ):
        super().__init__()
        self.ln1 = nn.LayerNorm(d_model)
        self.attn = MultiHeadCausalAttention(d_model, n_heads, dropout=dropout)
        self.ln2 = nn.LayerNorm(d_model)
        self.ffn = FeedForward(d_model, ffn_hidden, dropout=dropout)

    def forward(self, x: torch.Tensor) -> torch.Tensor:
        # x: (B, T, d_model)
        # First sub-layer: attention with residual + pre-norm.
        x = x + self.attn(self.ln1(x))
        # Second sub-layer: FFN with residual + pre-norm.
        x = x + self.ffn(self.ln2(x))
        return x


def _smoke_test() -> None:
    print("=" * 60)
    print("smoke test — TransformerBlock shapes and gradient flow")
    print("=" * 60)
    torch.manual_seed(0)

    block = TransformerBlock(
        d_model=MODEL.d_model,
        n_heads=MODEL.n_heads,
        ffn_hidden=MODEL.ffn_hidden,
    )
    x = torch.randn(2, 16, MODEL.d_model, requires_grad=True)
    y = block(x)
    print(f"  input:  {tuple(x.shape)}")
    print(f"  output: {tuple(y.shape)}")
    assert y.shape == x.shape

    # Gradient sanity — every parameter gets a non-zero gradient from a
    # scalar sum-of-output loss. If any has zero grad, residual or LN is
    # routed wrong.
    loss = y.sum()
    loss.backward()
    zero_grads = [n for n, p in block.named_parameters() if p.grad is None or p.grad.abs().sum() == 0]
    if zero_grads:
        print(f"  ⚠️  params with zero grad: {zero_grads}")
    else:
        print("  every param received non-zero gradient ✓")

    n_params = sum(p.numel() for p in block.parameters())
    attn_params = sum(p.numel() for p in block.attn.parameters())
    ffn_params = sum(p.numel() for p in block.ffn.parameters())
    print(f"\n  block params: {n_params:,}")
    print(f"    attention: {attn_params:,}  ({100 * attn_params / n_params:.1f}%)")
    print(f"    ffn:       {ffn_params:,}  ({100 * ffn_params / n_params:.1f}%)")
    print(f"    layernorms: {n_params - attn_params - ffn_params:,}  (rest)")


if __name__ == "__main__":
    _smoke_test()
