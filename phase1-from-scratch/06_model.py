"""Step 6 — The full GPT-style decoder-only model.

Composes everything from steps 3-5:

  ids ─→ InputEmbedding ─→ Block₁ ─→ Block₂ ─→ ... ─→ Blockₙ ─→ LN ─→ Head ─→ logits

The LM Head is a `Linear(d_model → vocab_size)` that we weight-tie to
the input embedding (same weight matrix used both for embedding and
output projection). Saves vocab_size × d_model parameters and tends to
slightly improve quality.

Run directly to construct the model and print a parameter inventory:

    python phase1-from-scratch/06_model.py
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

_emb_mod = __import__("03_embeddings")
_blk_mod = __import__("05_block")
InputEmbedding = _emb_mod.InputEmbedding
TransformerBlock = _blk_mod.TransformerBlock


class GPT(nn.Module):
    """Decoder-only transformer. Same architecture family as GPT-2/3,
    Llama, Phi, Gemma — just smaller.
    """

    def __init__(
        self,
        vocab_size: int = MODEL.vocab_size,
        seq_len: int = MODEL.seq_len,
        d_model: int = MODEL.d_model,
        n_layers: int = MODEL.n_layers,
        n_heads: int = MODEL.n_heads,
        ffn_hidden: int = MODEL.ffn_hidden,
        dropout: float = MODEL.dropout,
    ):
        super().__init__()
        self.input_emb = InputEmbedding(vocab_size, seq_len, d_model)
        self.blocks = nn.ModuleList([
            TransformerBlock(d_model, n_heads, ffn_hidden, dropout=dropout)
            for _ in range(n_layers)
        ])
        self.final_ln = nn.LayerNorm(d_model)

        # Weight-tied LM head. We don't define a separate nn.Linear here —
        # the forward pass uses `input_emb.token.weight.T` directly. This
        # saves vocab_size * d_model parameters and is the standard
        # practice since GPT-2.
        self.vocab_size = vocab_size
        self.seq_len = seq_len

    # ------------------------------------------------------------------
    # Forward
    # ------------------------------------------------------------------

    def forward(
        self,
        token_ids: torch.Tensor,
        targets: torch.Tensor | None = None,
    ) -> tuple[torch.Tensor, torch.Tensor | None]:
        """Two return shapes:
            (logits, None)            when targets is None — pure inference
            (logits, loss)            when targets is given — training
        """
        # token_ids: (B, T)
        # targets:   (B, T) shifted by 1, same dtype as token_ids
        x = self.input_emb(token_ids)            # (B, T, d_model)
        for block in self.blocks:
            x = block(x)
        x = self.final_ln(x)                      # (B, T, d_model)

        # Weight-tied LM head: (B, T, d_model) @ (d_model, vocab_size).
        # We use the SAME matrix as the input embedding (transposed).
        logits = x @ self.input_emb.token.weight.T  # (B, T, vocab_size)

        if targets is None:
            return logits, None

        # CrossEntropy expects (N, C) and (N,) so we flatten the batch
        # and time dims. Reduction defaults to mean which is what every
        # training loop you've ever read assumes.
        loss = torch.nn.functional.cross_entropy(
            logits.view(-1, self.vocab_size),
            targets.view(-1),
        )
        return logits, loss

    # ------------------------------------------------------------------
    # Convenience helpers
    # ------------------------------------------------------------------

    def n_params(self, *, include_embedding: bool = True) -> int:
        """Count parameters. Pass include_embedding=False for the
        "non-embedding" param count people quote when bragging about
        small models (it's a hack to make the number look smaller)."""
        total = sum(p.numel() for p in self.parameters())
        if not include_embedding:
            total -= self.input_emb.token.weight.numel()
        return total


def _smoke_test() -> None:
    print("=" * 60)
    print("smoke test — full GPT model")
    print("=" * 60)
    torch.manual_seed(0)
    model = GPT()
    print(f"  vocab_size: {MODEL.vocab_size}")
    print(f"  d_model:    {MODEL.d_model}")
    print(f"  n_layers:   {MODEL.n_layers}")
    print(f"  n_heads:    {MODEL.n_heads}")
    print(f"  seq_len:    {MODEL.seq_len}")
    print(f"  ffn_hidden: {MODEL.ffn_hidden}")

    total = model.n_params()
    non_emb = model.n_params(include_embedding=False)
    print(f"\n  total params:        {total:>14,}  ({total / 1e6:.1f}M)")
    print(f"  non-embedding params: {non_emb:>14,}  ({non_emb / 1e6:.1f}M)")
    print(f"  embedding fraction:   {(total - non_emb) / total * 100:.0f}%")
    print("  (high embedding fraction is normal at small d_model — when you")
    print("   scale d_model up, the blocks dominate.)")

    # Forward pass with both shapes.
    B, T = 2, 16
    ids = torch.randint(0, MODEL.vocab_size, (B, T))
    targets = torch.randint(0, MODEL.vocab_size, (B, T))

    logits, loss = model(ids, targets)
    print(f"\n  forward shapes:")
    print(f"    input ids:    {tuple(ids.shape)}")
    print(f"    output logits:{tuple(logits.shape)}")
    print(f"    loss scalar:  {loss.item():.4f}  (random init → near log({MODEL.vocab_size}) = {torch.tensor(MODEL.vocab_size).log().item():.3f})")
    assert logits.shape == (B, T, MODEL.vocab_size)

    # Just-inference path (no loss).
    logits_only, none = model(ids)
    assert none is None
    print(f"\n  inference-only call: logits shape {tuple(logits_only.shape)}, loss={none}")


if __name__ == "__main__":
    _smoke_test()
