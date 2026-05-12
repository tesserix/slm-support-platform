"""Step 2 — Byte-Pair Encoding (BPE) tokenizer from scratch.

Replaces the tiktoken placeholder used in `01_data.py`. The BPE algorithm
is small enough to be understood line-by-line:

  1. Start with a base vocabulary of every byte (256 tokens).
  2. Look at the training corpus as sequences of bytes.
  3. Find the most frequent adjacent pair of tokens.
  4. Add that pair as a NEW token (vocab grows by 1).
  5. Replace all occurrences of that pair with the new token.
  6. Repeat (3-5) until the vocab reaches the target size.

That's the entire algorithm. Everything fancy about modern tokenizers
(GPT-2, Llama, etc.) is engineering on top of this core loop —
pre-tokenization regex, special tokens, fast lookup tables, byte-level
fallback. We implement the core here.

Run directly to train + smoke-test:

    python phase1-from-scratch/02_tokenizer.py

It trains a small (~2000-token) BPE on a slice of TinyStories,
encodes/decodes a sample, and reports the compression ratio.
"""

from __future__ import annotations

import json
import re
import sys
import time
from collections import Counter
from pathlib import Path

# Add the local dir so we can import the data loader.
_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))


# A modest pre-tokenization regex. GPT-2/Llama use richer patterns but
# for TinyStories this is enough: split on whitespace, punctuation, and
# digits. The whitespace is kept ATTACHED to the next word (leading
# space) so the tokenizer learns "the" vs " the" as distinct tokens —
# the model's notion of word-boundary lives in the tokenizer.
_PRETOK = re.compile(r"\s*[\w]+|\s*[^\w\s]+|\s+", re.UNICODE)


def _pre_tokenize(text: str) -> list[bytes]:
    """Split text into pre-tokens, each as a bytes object.

    Returning bytes (not str) so the BPE core operates on integers
    0..255 — a clean fixed alphabet that round-trips through UTF-8.
    """
    return [m.group(0).encode("utf-8") for m in _PRETOK.finditer(text)]


class BPETokenizer:
    """Pure-Python BPE tokenizer. Slow but readable.

    Attributes:
        vocab: token-id -> bytes lookup. Always >= 256 entries (base bytes).
        merges: list of (pair, new_token_id) tuples in the order they were
                learned during training. Order matters for encoding —
                we apply merges in the same order on inference.
    """

    def __init__(self) -> None:
        self.vocab: dict[int, bytes] = {i: bytes([i]) for i in range(256)}
        self.merges: list[tuple[tuple[int, int], int]] = []

    # ------------------------------------------------------------------
    # Training
    # ------------------------------------------------------------------

    def train(self, corpus: str, vocab_size: int, *, verbose: bool = True) -> None:
        """Learn merges until vocab reaches vocab_size.

        corpus is a single string (e.g. concatenated TinyStories).
        vocab_size is the TARGET final vocab size (must be >= 256).
        """
        if vocab_size < 256:
            raise ValueError("vocab_size must be at least 256 (the base byte vocab)")

        # Pre-tokenize, then represent each pre-token as a list of integer
        # token IDs. Initial IDs are just the byte values (0..255).
        words: list[list[int]] = [list(t) for t in _pre_tokenize(corpus)]

        # Count how many times each pre-token occurs — merging identical
        # words once and re-using the count is the main perf win that
        # makes BPE training feasible on a laptop.
        # Tuples are hashable; lists aren't.
        word_counts = Counter(tuple(w) for w in words)
        # Convert back to a {tuple-key: [count, mutable_list]} structure
        # so we can mutate lists in place during merge passes.
        word_freq: dict[tuple[int, ...], int] = dict(word_counts)

        merges_to_do = vocab_size - len(self.vocab)
        start = time.time()

        for step in range(merges_to_do):
            # 1. Count adjacent pairs across the (unique) words.
            pair_counts: Counter[tuple[int, int]] = Counter()
            for word, freq in word_freq.items():
                for a, b in zip(word, word[1:]):
                    pair_counts[(a, b)] += freq

            if not pair_counts:
                if verbose:
                    print(f"  no more pairs after {step} merges; stopping early")
                break

            # 2. Pick the most common pair.
            best_pair, best_count = pair_counts.most_common(1)[0]
            new_id = 256 + step
            self.vocab[new_id] = self.vocab[best_pair[0]] + self.vocab[best_pair[1]]
            self.merges.append((best_pair, new_id))

            # 3. Re-write every word that contains best_pair, replacing
            #    occurrences with the new id.
            new_word_freq: dict[tuple[int, ...], int] = {}
            for word, freq in word_freq.items():
                if best_pair[0] not in word or best_pair[1] not in word:
                    new_word_freq[word] = new_word_freq.get(word, 0) + freq
                    continue
                merged = _apply_pair_replace(list(word), best_pair, new_id)
                key = tuple(merged)
                new_word_freq[key] = new_word_freq.get(key, 0) + freq
            word_freq = new_word_freq

            if verbose and (step % 200 == 0 or step == merges_to_do - 1):
                elapsed = time.time() - start
                print(
                    f"  merge {step + 1:4d}/{merges_to_do}  "
                    f"vocab={len(self.vocab):4d}  "
                    f"new={self.vocab[new_id]!r}  "
                    f"count={best_count}  "
                    f"elapsed={elapsed:.1f}s"
                )

    # ------------------------------------------------------------------
    # Encoding / decoding
    # ------------------------------------------------------------------

    def encode(self, text: str) -> list[int]:
        """Encode a string to a list of token ids.

        Apply learned merges in the order they were learned. This isn't
        the fastest possible algorithm (the production trick is a
        priority queue keyed by merge rank) but it's the easiest to
        understand.
        """
        out: list[int] = []
        for pre_token_bytes in _pre_tokenize(text):
            tokens = list(pre_token_bytes)
            for pair, new_id in self.merges:
                tokens = _apply_pair_replace(tokens, pair, new_id)
            out.extend(tokens)
        return out

    def decode(self, ids: list[int]) -> str:
        """Inverse of encode. Concatenate the byte sequences of each id
        and decode as UTF-8 (with 'replace' for safety on partial UTF-8).
        """
        raw = b"".join(self.vocab[i] for i in ids)
        return raw.decode("utf-8", errors="replace")

    # ------------------------------------------------------------------
    # Persistence
    # ------------------------------------------------------------------

    def save(self, path: str | Path) -> None:
        """Save merges to disk. The base 256-byte vocab is implicit so
        we only persist what we learned.
        """
        path = Path(path)
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w") as f:
            json.dump(
                {"merges": [[list(p), new] for p, new in self.merges]},
                f,
            )

    @classmethod
    def load(cls, path: str | Path) -> "BPETokenizer":
        with Path(path).open() as f:
            data = json.load(f)
        t = cls()
        for entry in data["merges"]:
            (a, b), new_id = entry
            t.merges.append(((a, b), new_id))
            t.vocab[new_id] = t.vocab[a] + t.vocab[b]
        return t

    @property
    def vocab_size(self) -> int:
        return len(self.vocab)


def _apply_pair_replace(tokens: list[int], pair: tuple[int, int], new_id: int) -> list[int]:
    """Walk through tokens left-to-right, replacing every occurrence of
    the consecutive pair with new_id. Used during both training (rewriting
    the corpus after each merge) and inference (applying merges to a
    new input).
    """
    out: list[int] = []
    i = 0
    while i < len(tokens):
        if i < len(tokens) - 1 and tokens[i] == pair[0] and tokens[i + 1] == pair[1]:
            out.append(new_id)
            i += 2
        else:
            out.append(tokens[i])
            i += 1
    return out


# ----------------------------------------------------------------------
# Smoke test entry point
# ----------------------------------------------------------------------


def _smoke_test() -> None:
    print("=" * 64)
    print("smoke test — train BPE on a slice of TinyStories")
    print("=" * 64)

    # Use the data loader from step 1 — we already have the cache.
    from importlib import import_module

    data_mod = import_module("01_data") if False else None  # noqa: F841 — pyflakes hint
    # Module names starting with a digit aren't importable directly; use
    # __import__ to keep the educational file numbering scheme.
    data = __import__("01_data")
    ds = data.TinyStoriesDataset(token_budget=500_000)
    # Reconstruct the text from the token cache (uses the placeholder
    # tiktoken encoding) — just to have a real-shaped string to train on.
    placeholder_enc = data.get_tokenizer()
    sample_text = placeholder_enc.decode(ds.tokens[: 200_000].tolist())
    print(f"  training on {len(sample_text):,} characters of TinyStories")

    tok = BPETokenizer()
    tok.train(sample_text, vocab_size=2000, verbose=True)
    print(f"\n  trained vocab size: {tok.vocab_size}")

    print("\n  sample encode / decode round-trip:")
    sample = "Once upon a time, a little girl named Lily found a red balloon."
    ids = tok.encode(sample)
    decoded = tok.decode(ids)
    print(f"    text:    {sample!r}")
    print(f"    ids[:10]: {ids[:10]}")
    print(f"    decoded: {decoded!r}")
    print(f"    n_tokens: {len(ids)}  (vs {len(sample)} chars)")
    print(f"    compression: {len(sample) / len(ids):.2f} chars/token")

    cache = _HERE / ".cache" / "tinystories_bpe_2k.json"
    tok.save(cache)
    print(f"\n  saved vocab + merges to {cache}")

    # Round-trip through disk to confirm the persistence works.
    loaded = BPETokenizer.load(cache)
    assert loaded.encode(sample) == ids, "round-trip mismatch"
    print("  load() round-trip verified")


if __name__ == "__main__":
    _smoke_test()
