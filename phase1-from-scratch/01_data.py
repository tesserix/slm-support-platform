"""Step 1 — Data loading for the Phase 1 SLM.

Downloads TinyStories from the Hugging Face datasets hub, tokenises it
with a placeholder GPT-2 BPE (step 2 swaps in our own), packs the tokens
into contiguous arrays, and exposes a torch Dataset that yields
(input_ids, target_ids) pairs where target_ids = input_ids shifted by
one — the canonical next-token-prediction setup.

Run directly for a smoke test:

    python phase1-from-scratch/01_data.py

It downloads/caches TinyStories on first run (~500MB), prints a sample,
and reports rough timings.
"""

from __future__ import annotations

import os
import time
from pathlib import Path

import numpy as np
import tiktoken
import torch
from torch.utils.data import Dataset

# Local import. When running this file directly, Python's working dir
# may not include phase1-from-scratch on sys.path, so we add it.
import sys

_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))
from config import MODEL, TRAIN  # noqa: E402


# Where tokenised data lands on disk so we don't re-tokenise on every run.
_CACHE_DIR = Path("phase1-from-scratch/.cache")
_CACHE_DIR.mkdir(parents=True, exist_ok=True)
_TOKENS_CACHE = _CACHE_DIR / "tinystories_tokens.bin"


def get_tokenizer():
    """Return a tiktoken GPT-2 encoding — placeholder for step 2.

    GPT-2 BPE has 50_257 tokens. Matches MODEL.vocab_size in config.py.
    """
    return tiktoken.get_encoding("gpt2")


def _download_and_tokenise(token_budget: int) -> np.ndarray:
    """Download TinyStories, tokenise it, return a flat uint32 array of
    token ids capped at token_budget.

    Cached on disk so subsequent runs are instant. The cache key only
    depends on token_budget — change the tokenizer and you must delete
    the cache by hand.
    """
    if _TOKENS_CACHE.exists():
        arr = np.fromfile(_TOKENS_CACHE, dtype=np.uint32)
        if arr.size >= token_budget:
            return arr[:token_budget]
        # Cache too small — fall through and re-tokenise.

    # Lazy import — datasets has heavy transitive deps we don't want
    # loaded when this module is imported by 02_tokenizer.py etc.
    from datasets import load_dataset

    enc = get_tokenizer()
    print(f"downloading TinyStories from HF hub (this may take a minute)...")
    ds = load_dataset("roneneldan/TinyStories", split="train", streaming=True)

    print(f"tokenising up to {token_budget:,} tokens...")
    out: list[int] = []
    eot = enc.eot_token  # 50256 for gpt2
    t0 = time.time()
    for row in ds:
        ids = enc.encode_ordinary(row["text"])
        out.extend(ids)
        out.append(eot)
        if len(out) >= token_budget:
            break
    arr = np.asarray(out[:token_budget], dtype=np.uint32)
    arr.tofile(_TOKENS_CACHE)
    elapsed = time.time() - t0
    print(f"tokenised {arr.size:,} tokens in {elapsed:.1f}s → {_TOKENS_CACHE}")
    return arr


class TinyStoriesDataset(Dataset):
    """Wraps a flat token array as (input, target) seq_len pairs.

    No fancy bucketing or padding — TinyStories text is plenty for a
    contiguous packed stream. Each call to __getitem__ returns a random
    seq_len window if shuffle_windows=True, else a deterministic stride.
    The training loop uses the random-window path; eval uses the
    deterministic one for reproducibility.
    """

    def __init__(
        self,
        token_budget: int = TRAIN.train_tokens,
        seq_len: int = MODEL.seq_len,
        shuffle_windows: bool = True,
        seed: int = 0,
    ):
        self.tokens = _download_and_tokenise(token_budget)
        self.seq_len = seq_len
        self.shuffle = shuffle_windows
        self._rng = np.random.default_rng(seed)

    def __len__(self) -> int:
        # When shuffling we don't care about exact length; pick a number
        # large enough that DataLoader workers don't churn.
        return max(1, (self.tokens.size - 1) // self.seq_len)

    def __getitem__(self, idx: int) -> tuple[torch.Tensor, torch.Tensor]:
        if self.shuffle:
            # Random offset into the stream, ensuring seq_len+1 tokens remain.
            start = int(self._rng.integers(0, self.tokens.size - self.seq_len - 1))
        else:
            start = idx * self.seq_len
        window = self.tokens[start : start + self.seq_len + 1]
        x = torch.from_numpy(window[:-1].astype(np.int64))
        y = torch.from_numpy(window[1:].astype(np.int64))
        return x, y


def _smoke_test() -> None:
    print("=" * 60)
    print("smoke test — building dataset with a small token budget")
    print("=" * 60)
    ds = TinyStoriesDataset(token_budget=1_000_000)
    print(f"dataset size: {len(ds):,} windows of {ds.seq_len} tokens")
    enc = get_tokenizer()
    x, y = ds[0]
    print(f"sample x shape: {tuple(x.shape)}, dtype: {x.dtype}")
    print(f"sample y shape: {tuple(y.shape)}, dtype: {y.dtype}")
    print(f"x[:10]:  {x[:10].tolist()}")
    print(f"y[:10]:  {y[:10].tolist()}  (should be x shifted by 1)")
    print(f"decoded sample: {enc.decode(x[:50].tolist())[:200]!r}")


if __name__ == "__main__":
    # Allow tests/runs to set HF cache dir without polluting the home dir.
    os.environ.setdefault("HF_DATASETS_CACHE", str(_CACHE_DIR / "hf"))
    _smoke_test()
