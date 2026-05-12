"""Step 8 — Sampling: greedy, temperature, top-k, top-p.

Generation is the inverse of training. At each step:

  1. Run the model on the current sequence → get logits for the next token
  2. Apply a sampling strategy to pick ONE id from those logits
  3. Append it and repeat

The sampling strategy is what makes generation interesting. Greedy
always picks the argmax — boring, repetitive, deterministic. Temperature
divides logits before softmax (T < 1 sharpens, T > 1 flattens). Top-k
keeps only the K most likely tokens. Top-p (nucleus) keeps the smallest
set of tokens whose probabilities sum to >= p.

All four live as standalone functions here so you can stack them
(temperature + top-p is the usual production combo).

Run:

    python phase1-from-scratch/08_generate.py --prompt "Once upon a time"
"""

from __future__ import annotations

import argparse
import sys
from pathlib import Path

import torch

_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from config import MODEL, TRAIN  # noqa: E402

_data = __import__("01_data")
_model = __import__("06_model")
GPT = _model.GPT


# ----------------------------------------------------------------------
# Sampling primitives
# ----------------------------------------------------------------------


def greedy(logits: torch.Tensor) -> int:
    """Pick the argmax token. Deterministic, repetitive."""
    return int(logits.argmax().item())


def temperature_sample(logits: torch.Tensor, *, temperature: float = 1.0) -> int:
    """Sample after temperature scaling. T -> 0 approaches greedy."""
    if temperature <= 0:
        return greedy(logits)
    probs = torch.softmax(logits / temperature, dim=-1)
    return int(torch.multinomial(probs, num_samples=1).item())


def top_k_sample(logits: torch.Tensor, *, k: int, temperature: float = 1.0) -> int:
    """Keep only the K highest-logit tokens; sample from the renormalised
    distribution after temperature scaling.
    """
    if k <= 0:
        return temperature_sample(logits, temperature=temperature)
    # Mask everything outside the top-k to -inf
    top_vals, top_idx = torch.topk(logits, k)
    mask = torch.full_like(logits, float("-inf"))
    mask.scatter_(-1, top_idx, top_vals)
    return temperature_sample(mask, temperature=temperature)


def top_p_sample(logits: torch.Tensor, *, p: float = 0.9, temperature: float = 1.0) -> int:
    """Nucleus sampling. Keep the smallest set of tokens whose cumulative
    probability >= p, then renormalise and sample.

    p=1 is equivalent to plain temperature sampling. p=0.9 is a common
    production choice — most of the probability mass with much less risk
    of picking an oddball low-probability token.
    """
    if p >= 1.0 or p <= 0.0:
        return temperature_sample(logits, temperature=temperature)
    if temperature <= 0:
        return greedy(logits)

    probs = torch.softmax(logits / temperature, dim=-1)
    sorted_probs, sorted_idx = torch.sort(probs, descending=True)
    cumulative = torch.cumsum(sorted_probs, dim=-1)
    # cutoff is the smallest index where cumulative >= p
    cutoff = int((cumulative >= p).nonzero(as_tuple=True)[0][0].item()) + 1
    nucleus = sorted_idx[:cutoff]
    nucleus_probs = sorted_probs[:cutoff]
    nucleus_probs = nucleus_probs / nucleus_probs.sum()
    chosen = int(torch.multinomial(nucleus_probs, num_samples=1).item())
    return int(nucleus[chosen].item())


# ----------------------------------------------------------------------
# Generation loop
# ----------------------------------------------------------------------


@torch.no_grad()
def generate(
    model: GPT,
    prompt_ids: list[int],
    *,
    max_new: int = 100,
    temperature: float = 0.8,
    top_p: float = 0.9,
    top_k: int | None = None,
    device: torch.device | None = None,
) -> list[int]:
    """Autoregressive generation. Stops after max_new tokens."""
    model.eval()
    device = device or next(model.parameters()).device
    ids = torch.tensor(prompt_ids, dtype=torch.long, device=device).unsqueeze(0)

    for _ in range(max_new):
        window = ids[:, -model.seq_len :]
        logits, _ = model(window)
        last_logits = logits[0, -1]

        if top_k:
            next_id = top_k_sample(last_logits, k=top_k, temperature=temperature)
        else:
            next_id = top_p_sample(last_logits, p=top_p, temperature=temperature)

        ids = torch.cat([ids, torch.tensor([[next_id]], device=device)], dim=1)

    return ids[0].tolist()


# ----------------------------------------------------------------------
# CLI
# ----------------------------------------------------------------------


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--prompt", default="Once upon a time")
    parser.add_argument("--checkpoint", default=str(Path(TRAIN.checkpoint_dir) / "final.pt"))
    parser.add_argument("--max-new", type=int, default=100)
    parser.add_argument("--temperature", type=float, default=0.8)
    parser.add_argument("--top-p", type=float, default=0.9)
    parser.add_argument("--top-k", type=int, default=0)
    args = parser.parse_args()

    device = "cuda" if torch.cuda.is_available() else ("mps" if torch.backends.mps.is_available() else "cpu")
    print(f"device: {device}")

    model = GPT().to(device)
    ckpt = Path(args.checkpoint)
    if ckpt.exists():
        print(f"loading checkpoint {ckpt}")
        state = torch.load(ckpt, map_location=device, weights_only=False)
        model.load_state_dict(state["model"])
    else:
        print(f"⚠️  {ckpt} not found — running with random init (output will be gibberish)")

    enc = _data.get_tokenizer()  # placeholder tiktoken; swap for step-2 BPE once trained
    prompt_ids = enc.encode_ordinary(args.prompt)
    print(f"prompt: {args.prompt!r}  ({len(prompt_ids)} tokens)")

    out = generate(
        model,
        prompt_ids,
        max_new=args.max_new,
        temperature=args.temperature,
        top_p=args.top_p,
        top_k=args.top_k or None,
        device=torch.device(device),
    )
    text = enc.decode(out)
    print("\n--- generated ---")
    print(text)


if __name__ == "__main__":
    main()
