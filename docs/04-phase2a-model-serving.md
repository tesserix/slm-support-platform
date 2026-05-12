# 04 — Phase 2A: Model & Serving Optimizations

Goal of Phase 2A: take a real open-weight SLM (3.8B params or so), fine-tune it on Tesserix data, and serve it fast and cheap enough for live customer support — sub-500ms p95 latency, under $0.01 per ticket, on a single L4 GPU in our GKE cluster.

This phase is structured as a sequence of optimizations layered on top of each other. Each step adds one technique, measures its effect, and moves on. The point isn't just to end up with a fast system; it's to understand *which* of these techniques bought how much speed and why.

## The optimization stack (top-down view)

```
                Latency / cost
                     ^
                     |  worst
   no optimizations  |  ~10 t/s, ~3s/request, single-stream
                     |
   + KV cache        |  ~30 t/s (most of the win)
                     |
   + FlashAttention-2|  ~40 t/s, less GPU memory
                     |
   + int8 quantize   |  ~60 t/s, half the memory
                     |
   + continuous batch|  10x throughput at constant latency
                     |
   + speculative dec.|  +1.5x to +3x latency reduction
                     |  best
                     v
```

Each step in this doc maps to one of those layers.

## Step-by-step plan

### Step 1 — Pick the base model

You're choosing among small open-weight chat-tuned models. The shortlist:

| Model | Params | License | Strengths | Weaknesses |
|-------|--------|---------|-----------|------------|
| **Phi-3-mini-128k-instruct** | 3.8B | MIT | Strong reasoning per parameter, long context | Microsoft tuning may be too "assistant-flavoured" for support |
| **Llama-3.2-3B-Instruct** | 3.2B | Llama license | Strong general quality, big community | License has acceptable-use restrictions to read |
| **Gemma-2-2B-it** | 2.6B | Gemma license | Smallest, fastest inference | Slightly weaker on benchmarks |
| **Qwen2.5-1.5B-Instruct** | 1.5B | Apache 2.0 | Fastest, cleanest license | Quality drops on complex queries |

Pick by running each through a short eval set of Tesserix-style support questions *without* fine-tuning. The base-model winner usually wins after fine-tuning too. Default recommendation: **Phi-3-mini** for quality, **Qwen2.5-1.5B** for cost-sensitive scale.

**Files:** `2a-model-serving/01_base_model_select.md` (notes, not code — record the bake-off results)

### Step 2 — Assemble training data

LoRA on a 3B model needs roughly 1k–10k high-quality examples to noticeably shift behaviour. Don't try to compete with the base model on general knowledge — you'll lose. Adapt only what the base model can't do:

- **Product vocabulary.** A few hundred Q/A pairs per product that use real product terminology (mark8ly "drop," fanzone "match," HomeChef "chef payout").
- **Tone.** A few hundred examples of "the Tesserix way" of replying: warm, concise, action-oriented, no hedging.
- **Refusals.** Examples of "I can't answer that, here's how to reach a human" — out-of-scope queries.
- **Tool-calling format.** Examples where the right answer is an MCP tool call, so the model learns to emit tool calls in the right shape.

Sources: existing support transcripts (anonymised), product docs (rewritten as Q/A), synthetic generation by a larger model (GPT-4 or Claude) seeded from real questions then human-reviewed.

**Files:** `2a-model-serving/02_training_data.py`

### Step 3 — LoRA / QLoRA fine-tune

LoRA (Low-Rank Adaptation) doesn't update the model's weights. Instead, it adds tiny "adapter" matrices alongside the existing weights and only trains those. For a 3.8B model, LoRA trains roughly 0.1% of the parameters — meaning fine-tuning fits on a single 24GB GPU instead of needing multi-GPU full fine-tune.

QLoRA goes further: load the base model in 4-bit, train LoRA adapters in fp16 on top. Fits a 7B model on a single 16GB GPU.

Hyperparameters to think about (not blindly copy):

- **Rank `r`** — usually 8 or 16. Higher = more capacity, more risk of overfitting.
- **Alpha** — scaling factor, typically `2*r`.
- **Target modules** — which layers get adapters. Default to attention projections (q, k, v, o); LoRA papers show diminishing returns from adapting FFN layers.
- **Learning rate** — 1e-4 to 2e-4 is typical; too high catastrophically destroys the base model.
- **Epochs** — 1–3 epochs over a few thousand examples is enough. More memorises noise.

**What you'll learn:** why "adapter" methods work — the intrinsic dimensionality of fine-tuning task changes is low, so a low-rank perturbation suffices. Why full fine-tunes are usually overkill.

**Files:** `2a-model-serving/03_lora_finetune.py`

### Step 4 — KV cache (deep dive)

In Phase 1 you noticed generating each new token re-computes attention over *every* prior token. The KV cache is the fix: store the projected K and V tensors of every prior token, at every layer. Generating the next token then only computes K and V for *one* new token, and attends against the cached values.

What this costs in memory:

```
KV cache size = 2 (K and V) × n_layers × seq_len × n_heads × d_head × 2 bytes (fp16)
```

For Phi-3-mini (n_layers=32, n_heads=32, d_head=96), 2k tokens: ~800 MB. For 8k tokens: ~3.2 GB. This is the dominant memory cost in long-context inference.

Modern optimizations to study:

- **Paged Attention (vLLM).** Like virtual memory for KV cache. Don't allocate contiguous memory per request; allocate fixed-size blocks. Lets you serve many concurrent requests without fragmentation.
- **Prefix caching.** If multiple requests share the same prefix (system prompt + RAG context), cache the KV for the prefix once and reuse it across requests. Huge win for a chatbot where every request has a similar system prompt.

**What you'll learn:** why long context is expensive (KV grows linearly with seq_len, and attention compute is quadratic). Why paged attention matters for throughput. When prefix caching helps and when it doesn't.

**Files:** `2a-model-serving/04_kv_cache.py` — first implement a standalone naive KV cache to confirm understanding; then move to vLLM and benchmark its paged attention.

### Step 5 — Quantization

Quantization stores model weights in fewer bits — int8 instead of fp16 (half the memory) or int4 (quarter the memory). The tradeoff is a small accuracy loss.

The main approaches:

| Method | Bits | Quality loss | Speed gain | Notes |
|--------|------|-------------|------------|-------|
| **fp16 (no quant)** | 16 | none | baseline | starting point |
| **int8 (bitsandbytes)** | 8 | minimal | ~1.5–2x | easiest, in-memory quantization |
| **GPTQ** | 4 | small | ~2x | calibration data needed, slow to quantize once |
| **AWQ** | 4 | small | ~2x | similar to GPTQ, more recent, easier tuning |
| **GGUF (llama.cpp)** | 2–8 | varies | CPU-friendly | for non-GPU deployment, not our path |

Default for production GPU serving: **AWQ int4**. Run the quantized model through your eval set after quantizing — quality should drop by <2% on most benchmarks; if it drops more, your calibration data was off.

**What you'll learn:** why int4 works at all (most of the information in weights is in the high-order bits; round-off is mostly noise). Why activation quantization (also int8/int4) is much harder than weight quantization (activations have outliers).

**Files:** `2a-model-serving/05_quantize.py`

### Step 6 — Continuous batching

Naive serving: process one request at a time. The GPU is idle most of each request's lifetime because of memory-bound steps (loading KV cache, sampling).

Continuous batching: dynamically pack multiple in-flight requests into a single forward pass. When one request finishes a token, it stays in the batch; when it finishes generating, a new request slides into its slot. The batch size *varies* per step.

Result: 5–10x throughput at the same per-request latency — because the GPU's compute units were sitting idle anyway, you're filling them with work from other users.

vLLM and TGI both implement this. The interesting work in this step isn't writing it (vLLM does it for you); it's measuring it and understanding *why* throughput jumps so dramatically.

**Files:** `2a-model-serving/06_batching.py` — benchmark single-stream vs batched throughput.

### Step 7 — FlashAttention

Standard attention computes the full `n × n` attention matrix in GPU memory, even though the softmax-of-it is what you actually need. FlashAttention computes the attention output in tiles, never materialising the full matrix in HBM. Result: 2–4x faster attention and dramatically less memory at long context.

FlashAttention-2 (the current version) is already integrated into PyTorch, HuggingFace Transformers, and vLLM. You enable it with a flag.

The interesting work: confirm it's actually being used (don't take a config flag's word for it), and benchmark how much memory you save at 4k vs 8k vs 16k context.

**Files:** `2a-model-serving/07_flash_attention.py`

### Step 8 — Speculative decoding

A small "draft" model generates K tokens. The main "target" model verifies them in a single forward pass. Tokens the target accepts are kept; the first rejection becomes the next real token. Net effect: 1.5x to 3x faster generation, because verifying K tokens in one batched forward pass is much cheaper than generating K tokens sequentially.

Draft-model choices:

- **A smaller model from the same family.** Llama-3.2-1B as draft for Llama-3.3-8B target. Tokens align naturally.
- **N-gram speculation.** No draft model; predict next tokens from a lookup table of recent text. Cheap but only helps on repetitive patterns.
- **Self-speculation (e.g., Medusa).** Add small prediction heads to the target model itself. No separate draft model.

For our scale, n-gram speculation is probably enough — support chat has lots of common phrases ("your order", "please contact"). Try it before committing to a separate draft model.

**Files:** `2a-model-serving/08_speculative_decoding.py`

### Step 9 — vLLM serving config

By this point all the individual optimizations are working. vLLM ties them together. Production config to tune:

- `--tensor-parallel-size 1` (single L4) or 2 (if you go A100)
- `--max-model-len 4096` — don't pay KV cache cost for contexts you won't see
- `--gpu-memory-utilization 0.90` — let vLLM use most of the GPU
- `--enable-prefix-caching` — huge for repeated system prompts
- `--quantization awq` — match your quantization method
- `--speculative-model` or `--speculative-config` — wire in the draft model

Deploy on GKE as a Knative service in the `support-platform` namespace, same patterns as the other Tesserix services. Helm chart lives in `tesserix-k8s/charts/apps/support-platform/`.

**Files:** `2a-model-serving/09_vllm_serve.py` — Dockerfile + entrypoint + config; Helm chart goes in `tesserix-k8s`.

### Step 10 — Benchmark everything together

After each optimization step you took a localised measurement. Now do the end-to-end benchmark on representative traffic:

- **Latency:** p50 / p95 / p99 time to first token and total response time
- **Throughput:** tokens/second/GPU, requests/second/GPU
- **Cost:** $/M tokens generated (compute the GPU cost amortised over throughput)
- **Quality:** held-out eval set; per-product metric; refusal-rate sanity check

Target outcomes:
- p95 latency < 500ms for short responses (most support replies)
- > 200 tokens/sec/GPU
- < $0.001 per 1k-token reply
- Quality score on eval set within 5% of un-quantized fine-tuned model

**Files:** `2a-model-serving/10_benchmark.py`

## Target outcomes for Phase 2A

By the end you should be able to answer, without looking it up:

- Why does KV cache scale linearly with seq_len but compute scale quadratically?
- Why does int4 quantization barely hurt quality but int4 *activation* quantization is dangerous?
- Why does continuous batching dramatically improve throughput but barely move single-request latency?
- When does speculative decoding help and when is the draft model overhead bigger than the win?
- Why does prefix caching matter so much for a support chatbot specifically?

When those feel obvious, Phase 2A is done.

## What we're explicitly NOT doing in Phase 2A

- **Multi-GPU tensor parallelism.** Our model fits on one L4. Save the complexity.
- **Reinforcement learning from human feedback (RLHF) or DPO.** LoRA fine-tuning is enough for this stage.
- **Training a custom model from scratch.** That was Phase 1's pedagogical exercise. In production, we stand on the shoulders of Phi-3 / Llama / Gemma.
- **Mixture-of-experts (MoE).** No small good MoE models yet at our scale.

## Dependency on Phase 2B

None. 2A optimises model + inference. 2B optimises retrieval + indexing. They share nothing technical and can run in parallel.

Next: [`05-phase2b-retrieval-indexing.md`](05-phase2b-retrieval-indexing.md).
