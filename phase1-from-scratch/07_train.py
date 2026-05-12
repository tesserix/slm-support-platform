"""Step 7 — Training loop.

Pulls together the dataset (step 1), tokenizer (step 2, via placeholder),
embeddings (step 3), attention (step 4), block (step 5), model (step 6),
and trains the model on TinyStories.

What you get for free here once you understand each piece:

  - AdamW optimizer with weight decay
  - Warmup-then-cosine learning rate schedule (the GPT-2 / Llama default)
  - Gradient clipping at norm=1.0 (prevents the occasional outlier
    gradient from blowing up the next step)
  - Periodic eval loss + sample generation so you can see the model get
    less garbage over time
  - Checkpoint save every N steps

The training is single-GPU (or CPU / MPS) — no distributed plumbing.

Run:

    python phase1-from-scratch/07_train.py
"""

from __future__ import annotations

import math
import sys
import time
from pathlib import Path

import torch
from torch.utils.data import DataLoader

_HERE = Path(__file__).resolve().parent
if str(_HERE) not in sys.path:
    sys.path.insert(0, str(_HERE))

from config import MODEL, TRAIN  # noqa: E402

_data = __import__("01_data")
_model = __import__("06_model")
TinyStoriesDataset = _data.TinyStoriesDataset
GPT = _model.GPT


def pick_device() -> torch.device:
    """Pick the fastest available device. MPS on Apple Silicon, CUDA on
    NVIDIA, CPU otherwise.
    """
    if torch.cuda.is_available():
        return torch.device("cuda")
    if torch.backends.mps.is_available():
        return torch.device("mps")
    return torch.device("cpu")


def cosine_lr(step: int, *, max_lr: float, warmup: int, total: int, min_lr_frac: float = 0.1) -> float:
    """Linear warmup for `warmup` steps, then cosine decay over the rest.

    Returns the LR for this step. Cap at min_lr_frac * max_lr so the
    LR never drops to zero (which would freeze training if you ran
    past `total`).
    """
    if step < warmup:
        return max_lr * (step + 1) / warmup
    progress = (step - warmup) / max(1, total - warmup)
    progress = min(progress, 1.0)
    cos = 0.5 * (1.0 + math.cos(math.pi * progress))
    return max_lr * (min_lr_frac + (1 - min_lr_frac) * cos)


def make_optimizer(model: torch.nn.Module, lr: float, weight_decay: float) -> torch.optim.Optimizer:
    """AdamW with weight decay ONLY on 2D matrices.

    Biases and LayerNorm scales are 1D — applying weight decay to them
    is a common bug that hurts performance slightly. We split params
    into a decayed group and a non-decayed group.
    """
    decay, no_decay = [], []
    for name, p in model.named_parameters():
        if p.dim() >= 2:
            decay.append(p)
        else:
            no_decay.append(p)
    return torch.optim.AdamW(
        [
            {"params": decay, "weight_decay": weight_decay},
            {"params": no_decay, "weight_decay": 0.0},
        ],
        lr=lr,
        betas=(0.9, 0.95),  # GPT-3 / Llama default — lower beta2 helps stability
        eps=1e-8,
    )


def _sample_generation(model: GPT, device: torch.device, prompt_ids: list[int], max_new: int = 80) -> list[int]:
    """Greedy sampling. Step 8 has the proper temperature / top-k / top-p
    versions; for the in-training preview, greedy is fine.
    """
    model.eval()
    ids = torch.tensor(prompt_ids, dtype=torch.long, device=device).unsqueeze(0)
    with torch.no_grad():
        for _ in range(max_new):
            # Trim to last seq_len tokens — model's positional embedding
            # can't extend past that.
            window = ids[:, -model.seq_len :]
            logits, _ = model(window)
            next_id = logits[0, -1].argmax().item()
            ids = torch.cat([ids, torch.tensor([[next_id]], device=device)], dim=1)
    model.train()
    return ids[0].tolist()


def train() -> None:
    print("=" * 64)
    print("training run")
    print("=" * 64)

    device = pick_device()
    print(f"  device: {device}")

    torch.manual_seed(0)
    ds = TinyStoriesDataset(token_budget=TRAIN.train_tokens)
    print(f"  dataset: {len(ds):,} windows of {ds.seq_len} tokens")

    loader = DataLoader(
        ds,
        batch_size=TRAIN.batch_size,
        shuffle=False,  # the Dataset already randomises windows internally
        num_workers=2,
        pin_memory=(device.type == "cuda"),
        drop_last=True,
    )

    model = GPT().to(device)
    n_params = model.n_params()
    print(f"  model: {n_params / 1e6:.1f}M params")

    opt = make_optimizer(model, lr=TRAIN.learning_rate, weight_decay=TRAIN.weight_decay)

    ckpt_dir = Path(TRAIN.checkpoint_dir)
    ckpt_dir.mkdir(parents=True, exist_ok=True)

    loss_ewma = None
    step = 0
    start = time.time()

    # Persistent iterator so we can keep pulling batches across "epochs"
    # without DataLoader teardown overhead.
    it = iter(loader)

    while step < TRAIN.total_steps:
        try:
            x, y = next(it)
        except StopIteration:
            it = iter(loader)
            x, y = next(it)
        x = x.to(device, non_blocking=True)
        y = y.to(device, non_blocking=True)

        # Set LR for this step
        lr = cosine_lr(
            step,
            max_lr=TRAIN.learning_rate,
            warmup=TRAIN.warmup_steps,
            total=TRAIN.total_steps,
        )
        for g in opt.param_groups:
            g["lr"] = lr

        logits, loss = model(x, y)
        opt.zero_grad(set_to_none=True)
        loss.backward()
        torch.nn.utils.clip_grad_norm_(model.parameters(), TRAIN.grad_clip)
        opt.step()

        l = loss.item()
        loss_ewma = l if loss_ewma is None else 0.95 * loss_ewma + 0.05 * l

        if step % TRAIN.log_every == 0 or step == TRAIN.total_steps - 1:
            elapsed = time.time() - start
            tok_per_s = (step + 1) * TRAIN.batch_size * ds.seq_len / max(elapsed, 1e-6)
            print(
                f"  step {step:5d}/{TRAIN.total_steps}  "
                f"loss={l:.4f}  ewma={loss_ewma:.4f}  "
                f"lr={lr:.2e}  "
                f"tok/s={tok_per_s:,.0f}  "
                f"elapsed={elapsed:.0f}s"
            )

        if step > 0 and step % TRAIN.sample_every == 0:
            sample = _sample_generation(model, device, prompt_ids=[ds.tokens[0]], max_new=40)
            # Decode via the placeholder GPT-2 tiktoken used by the
            # data loader. Real BPE from step 2 would slot in here.
            enc = _data.get_tokenizer()
            print(f"    sample: {enc.decode(sample[:60])!r}")

        if step > 0 and step % TRAIN.save_every == 0:
            torch.save(
                {"step": step, "model": model.state_dict(), "opt": opt.state_dict()},
                ckpt_dir / f"step_{step:06d}.pt",
            )

        step += 1

    print(f"\n  done in {(time.time() - start) / 60:.1f} min, final ewma loss = {loss_ewma:.4f}")
    torch.save(
        {"step": step, "model": model.state_dict(), "opt": opt.state_dict()},
        ckpt_dir / "final.pt",
    )
    print(f"  saved final checkpoint to {ckpt_dir / 'final.pt'}")


if __name__ == "__main__":
    train()
