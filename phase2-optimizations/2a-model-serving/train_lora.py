"""Per-tenant LoRA fine-tune of qwen2.5-1.5b-instruct on exported
Otto conversations.

This is the script you run when a tenant has accumulated enough
closed conversations (rule of thumb: ~5k closed + resolved). It
reads the JSONL files the support-platform-export CronJob writes to
GCS, builds an instruction-tuned dataset (system prompt + customer
message -> staff/assistant reply), and trains a LoRA adapter on the
shared base model.

Usage:
    python train_lora.py --tenant fanzone --since 2026-04-01

Outputs a LoRA adapter to ./out/<tenant>-lora/ which is then:
1. Converted to GGUF + uploaded to the support-platform-slm-inference
   model bucket (see scripts/upload_adapter.sh — TODO).
2. Wired up by adding `lora_adapter` to the tenant's block in
   charts/apps/support-platform-slm-router/values.yaml.

Hardware: ~A100-40GB for ~2h on 5k conversations. The shared base
stays unchanged; each tenant's LoRA is ~30MB.

Status: scaffolding. Needs:
- Real labelled outcomes per conversation (currently we infer
  outcome from `inactivity_closed_at` / `escalated_to_human` which
  is good enough for filtering but not for reward modelling).
- A held-out eval set per tenant.
- An eval harness that scores tenant-specific accuracy
  (e.g. fanzone: cricket-term coverage; horoscope: refusal-rate
  on medical/legal questions).
"""

from __future__ import annotations

import argparse
import dataclasses
import datetime as dt
import json
import os
import sys

# These imports are gated behind __name__ == "__main__" so the file
# can be inspected (e.g. by `python -m py_compile`) on a host that
# doesn't have the ML stack installed. Required deps:
#   pip install transformers>=4.46 peft>=0.13 accelerate>=1.1 \
#               datasets>=3.1 bitsandbytes>=0.44 google-cloud-storage


@dataclasses.dataclass
class TrainConfig:
    tenant: str
    base_model: str = "Qwen/Qwen2.5-1.5B-Instruct"
    gcs_bucket: str = "tesseract-prod-otto-exports-in"
    output_dir: str = "./out"
    since: dt.date | None = None
    until: dt.date | None = None
    # LoRA hyperparams — tuned for ~1.5B base + ~5-50k conversations.
    # Larger r helps on bigger datasets; if the tenant has <2k
    # conversations consider r=8 to avoid overfit.
    lora_r: int = 16
    lora_alpha: int = 32
    lora_dropout: float = 0.05
    epochs: int = 2
    per_device_batch_size: int = 4
    gradient_accumulation_steps: int = 4
    learning_rate: float = 2e-4
    warmup_steps: int = 50
    seed: int = 42


# System prompts must match what slm-router will load at inference
# time. Mirror this to charts/apps/support-platform-slm-router/
# templates/configmap-prompts.yaml so a tenant's adapter is trained
# against the SAME prompt the inference server uses.
SYSTEM_PROMPTS: dict[str, str] = {
    "mark8ly": (
        "You are the Mark8ly marketplace support assistant. Use the "
        "mark8ly-mcp tools to look up orders, returns and payment "
        "methods before answering. Never guess an order status."
    ),
    "fanzone": (
        "You are the FanZone Battle Ground support assistant. Use "
        "the fanzone-mcp tools to look up matches, user points and "
        "prediction records. Never invent a score or a stat."
    ),
    "homechef": (
        "You are the HomeChef support assistant. Use the homechef-mcp "
        "tools to look up orders and delivery status. If the customer "
        "mentions illness, allergic reaction, or food poisoning, "
        "escalate to a human immediately."
    ),
    "stockpilot": (
        "You are the StockPilot support assistant. You are NOT a "
        "financial adviser. Do not recommend buying or selling "
        "specific securities. Escalate any investment-advice "
        "question to a human."
    ),
    "gameverse": (
        "You are the GameVerse support assistant. Use the gameverse-"
        "mcp tools to look up rooms and match history. Server-side "
        "moves are authoritative; never claim a match was rigged."
    ),
    "horoscope": (
        "You are the Tesserix Horoscope support assistant. Readings "
        "are for entertainment and self-reflection only. Never give "
        "medical, legal, financial, or safety advice. Escalate."
    ),
    "scrapper": (
        "You are the Social Media Scrapper support assistant. Do "
        "not assist with circumventing platform terms of service, "
        "scraping private content, or DM spam."
    ),
}


def load_examples(cfg: TrainConfig) -> list[dict]:
    """Stream JSONL files from gs://<bucket>/<tenant>/*.jsonl and
    yield one supervised example per (customer message -> next
    staff/assistant reply) pair. We deliberately train ONLY on
    resolved conversations; abandoned/escalated cases would teach
    the model to time out or escalate.
    """
    from google.cloud import storage

    gcs = storage.Client()
    bucket = gcs.bucket(cfg.gcs_bucket)

    prefix = f"{cfg.tenant}/"
    examples: list[dict] = []
    seen_files = 0

    for blob in bucket.list_blobs(prefix=prefix):
        if not blob.name.endswith(".jsonl"):
            continue
        # filename is <YYYY-MM-DD>.jsonl
        try:
            day = dt.date.fromisoformat(blob.name.removeprefix(prefix).removesuffix(".jsonl"))
        except ValueError:
            continue
        if cfg.since and day < cfg.since:
            continue
        if cfg.until and day > cfg.until:
            continue

        seen_files += 1
        data = blob.download_as_text()
        for line in data.splitlines():
            convo = json.loads(line)
            if convo.get("outcome") != "resolved":
                continue
            examples.extend(_pair_messages(convo))

    print(f"[{cfg.tenant}] read {seen_files} jsonl files -> {len(examples)} training pairs", file=sys.stderr)
    return examples


def _pair_messages(convo: dict) -> list[dict]:
    """Walk a conversation and emit one supervised pair per
    (customer turn -> staff/assistant turn). Multi-turn context is
    preserved as a list of prior messages.
    """
    out: list[dict] = []
    history: list[dict] = []
    for msg in convo.get("messages", []):
        role = msg.get("role")
        content = (msg.get("content") or "").strip()
        if not content:
            continue
        if role == "customer":
            history.append({"role": "user", "content": content})
        elif role in {"staff", "assistant"}:
            if history and history[-1]["role"] == "user":
                out.append(
                    {
                        "tenant": convo["tenant_id"],
                        "intake": convo.get("intake", {}),
                        "history": list(history),
                        "response": content,
                    }
                )
            history.append({"role": "assistant", "content": content})
        elif role == "system":
            # Skip lifecycle events.
            continue
    return out


def build_prompt(tenant: str, example: dict) -> str:
    """Render an example into the Qwen2.5 chat template."""
    system_prompt = SYSTEM_PROMPTS.get(tenant, "")
    parts: list[str] = []
    if system_prompt:
        parts.append(f"<|im_start|>system\n{system_prompt}<|im_end|>")
    for msg in example["history"]:
        parts.append(f"<|im_start|>{msg['role']}\n{msg['content']}<|im_end|>")
    parts.append(f"<|im_start|>assistant\n{example['response']}<|im_end|>")
    return "\n".join(parts)


def train(cfg: TrainConfig) -> str:
    """Run the LoRA fine-tune. Returns path to the saved adapter."""
    # Deferred imports — the ML stack is heavy.
    import torch
    from datasets import Dataset
    from peft import LoraConfig, get_peft_model, prepare_model_for_kbit_training
    from transformers import (
        AutoModelForCausalLM,
        AutoTokenizer,
        BitsAndBytesConfig,
        DataCollatorForLanguageModeling,
        Trainer,
        TrainingArguments,
    )

    examples = load_examples(cfg)
    if not examples:
        raise SystemExit(f"no training examples for tenant={cfg.tenant}")

    prompts = [build_prompt(cfg.tenant, e) for e in examples]

    tokenizer = AutoTokenizer.from_pretrained(cfg.base_model, trust_remote_code=True)
    tokenizer.pad_token = tokenizer.eos_token

    def encode(batch: dict) -> dict:
        return tokenizer(
            batch["text"],
            max_length=2048,
            truncation=True,
            padding=False,
        )

    ds = Dataset.from_dict({"text": prompts}).map(encode, batched=True, remove_columns=["text"])

    bnb = BitsAndBytesConfig(
        load_in_4bit=True,
        bnb_4bit_compute_dtype=torch.bfloat16,
        bnb_4bit_use_double_quant=True,
        bnb_4bit_quant_type="nf4",
    )
    base = AutoModelForCausalLM.from_pretrained(
        cfg.base_model,
        quantization_config=bnb,
        device_map="auto",
        trust_remote_code=True,
    )
    base = prepare_model_for_kbit_training(base)

    lora = LoraConfig(
        r=cfg.lora_r,
        lora_alpha=cfg.lora_alpha,
        lora_dropout=cfg.lora_dropout,
        bias="none",
        task_type="CAUSAL_LM",
        # Qwen2.5 attention modules.
        target_modules=["q_proj", "k_proj", "v_proj", "o_proj", "gate_proj", "up_proj", "down_proj"],
    )
    model = get_peft_model(base, lora)
    model.print_trainable_parameters()

    out_dir = os.path.join(cfg.output_dir, f"{cfg.tenant}-lora")
    args = TrainingArguments(
        output_dir=out_dir,
        num_train_epochs=cfg.epochs,
        per_device_train_batch_size=cfg.per_device_batch_size,
        gradient_accumulation_steps=cfg.gradient_accumulation_steps,
        learning_rate=cfg.learning_rate,
        warmup_steps=cfg.warmup_steps,
        bf16=True,
        logging_steps=10,
        save_strategy="epoch",
        seed=cfg.seed,
        report_to=[],
    )

    trainer = Trainer(
        model=model,
        args=args,
        train_dataset=ds,
        data_collator=DataCollatorForLanguageModeling(tokenizer=tokenizer, mlm=False),
    )
    trainer.train()

    model.save_pretrained(out_dir)
    tokenizer.save_pretrained(out_dir)
    print(f"saved LoRA adapter to {out_dir}")
    return out_dir


def main(argv: list[str]) -> int:
    parser = argparse.ArgumentParser(description="Train a per-tenant LoRA on Otto conversations")
    parser.add_argument("--tenant", required=True, choices=sorted(SYSTEM_PROMPTS.keys()))
    parser.add_argument("--since", type=dt.date.fromisoformat)
    parser.add_argument("--until", type=dt.date.fromisoformat)
    parser.add_argument("--output-dir", default="./out")
    parser.add_argument("--epochs", type=int, default=2)
    args = parser.parse_args(argv)

    cfg = TrainConfig(
        tenant=args.tenant,
        since=args.since,
        until=args.until,
        output_dir=args.output_dir,
        epochs=args.epochs,
    )
    train(cfg)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
