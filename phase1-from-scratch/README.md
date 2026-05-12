# Phase 1 — From-Scratch SLM

PyTorch code for the from-scratch decoder-only transformer. Files land here one step at a time, following [`../docs/03-phase1-build-plan.md`](../docs/03-phase1-build-plan.md).

## Planned files

```
phase1-from-scratch/
├── 01_data.py          download + load TinyStories
├── 02_tokenizer.py     BPE tokenizer
├── 03_embeddings.py    token + positional embeddings
├── 04_attention.py     scaled dot-product + multi-head + causal mask
├── 05_block.py         transformer block (attention + FFN + residual + LN)
├── 06_model.py         the full GPT-style model
├── 07_train.py         training loop with warmup + cosine LR
├── 08_generate.py      sampling: greedy / temperature / top-k / top-p
└── config.py           shared hyperparameters
```

Nothing is here yet — that's intentional. Read the docs first, then we'll write `01_data.py` together.

## Environment

We'll set up a `requirements.txt` and a venv when the first code file lands. Expected stack:

- Python 3.11+
- PyTorch 2.x (CUDA on Linux GPU, MPS on Apple Silicon, or CPU)
- `datasets` for TinyStories
- `tqdm`, `numpy`, `wandb` (optional logging)
