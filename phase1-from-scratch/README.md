# Phase 1 — From-Scratch SLM

PyTorch code for the from-scratch decoder-only transformer. Files land here one step at a time, following [`../docs/03-phase1-build-plan.md`](../docs/03-phase1-build-plan.md).

## Status

| File | Step | Status |
|------|------|--------|
| `requirements.txt` | env | ✅ |
| `config.py` | shared hyperparameters | ✅ |
| `01_data.py` | TinyStories download + Dataset | ✅ |
| `02_tokenizer.py` | BPE tokenizer | ✅ |
| `03_embeddings.py` | token + positional embeddings | ✅ |
| `04_attention.py` | scaled dot-product + multi-head + causal mask | ✅ |
| `05_block.py` | transformer block (attention + FFN + residual + LN) | ✅ |
| `06_model.py` | the full GPT-style model | ✅ |
| `07_train.py` | training loop with warmup + cosine LR | ✅ |
| `08_generate.py` | sampling: greedy / temperature / top-k / top-p | ✅ |

## End-to-end run

```bash
cd phase1-from-scratch
python 01_data.py          # download + tokenise TinyStories (one-time)
python 02_tokenizer.py     # train a BPE tokenizer (one-time, ~30s)
python 03_embeddings.py    # smoke-test the embedding layers
python 04_attention.py     # smoke-test attention + causality
python 05_block.py         # smoke-test one transformer block
python 06_model.py         # smoke-test the full model + param count
python 07_train.py         # train the model (CPU overnight / GPU 1-3h)
python 08_generate.py --prompt "Once upon a time"
```

## Environment setup

```bash
cd phase1-from-scratch
python -m venv .venv
source .venv/bin/activate
pip install -r requirements.txt
```

Then to smoke-test the data pipeline:

```bash
python 01_data.py
```

On first run it downloads ~500 MB of TinyStories, tokenises a million tokens, and prints a sample. Subsequent runs reuse the cached tokens.

## Hardware

Per `../docs/03-phase1-build-plan.md`:

| Setup | Time to train ~25M model on TinyStories |
|-------|-----------------------------------------|
| L4 / 3090 / 4090 | ~1–3 hours |
| Apple M-series (MPS backend) | ~6–10 hours |
| CPU only | overnight |
