"""Shared hyperparameters for the Phase 1 from-scratch SLM.

Single source of truth so every file imports the same numbers. Bump
these when scaling the model up or down — don't pass them around as
function arguments. The deliberately-small defaults target a ~25M-param
model trainable on a single GPU in ~2 hours (per docs/03-phase1-build-plan.md).
"""

from dataclasses import dataclass


@dataclass(frozen=True)
class ModelConfig:
    # Vocabulary size — depends on the tokenizer. tiktoken gpt2 = 50_257.
    # Our own BPE in step 2 targets ~2_000–10_000.
    vocab_size: int = 50_257

    # Width of the residual stream. The single most impactful knob.
    d_model: int = 256

    # Number of transformer blocks (depth).
    n_layers: int = 6

    # Number of attention heads. d_head = d_model // n_heads, so this
    # must divide d_model.
    n_heads: int = 8

    # Max context window. TinyStories sentences are short — 256 is plenty
    # and keeps attention's O(n^2) cost reasonable.
    seq_len: int = 256

    # FFN hidden dim. Standard transformer ratio is 4x.
    ffn_hidden: int = 4 * 256

    # Dropout. Small models on small data don't usually need much.
    dropout: float = 0.0

    @property
    def d_head(self) -> int:
        return self.d_model // self.n_heads


@dataclass(frozen=True)
class TrainConfig:
    # Training data subset size in number of tokens. TinyStories has
    # ~440M tokens; we use a slice so a single epoch fits in a few hours.
    train_tokens: int = 50_000_000

    # Batch size in sequences. seq_len * batch_size tokens per step.
    batch_size: int = 64

    # Optimizer.
    learning_rate: float = 3e-4
    weight_decay: float = 0.1
    grad_clip: float = 1.0

    # LR schedule.
    warmup_steps: int = 200
    total_steps: int = 5_000  # adjust based on how loss is moving

    # Logging cadence.
    log_every: int = 50
    eval_every: int = 500
    sample_every: int = 500
    save_every: int = 1_000

    # Where checkpoints land. Gitignored.
    checkpoint_dir: str = "phase1-from-scratch/checkpoints"


# Convenience singletons. Import and use directly:
#   from config import MODEL, TRAIN
MODEL = ModelConfig()
TRAIN = TrainConfig()
