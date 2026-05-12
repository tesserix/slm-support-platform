# Phase 2A — Model & Serving Optimizations (code)

Code for the model fine-tuning and inference-serving optimizations. Plan and step-by-step explanations: [`../../docs/04-phase2a-model-serving.md`](../../docs/04-phase2a-model-serving.md).

## Planned files

```
2a-model-serving/
├── 01_base_model_select.md      notes on the base-model bake-off
├── 02_training_data.py          assemble + clean the training corpus
├── 03_lora_finetune.py          LoRA / QLoRA fine-tune run
├── 04_kv_cache.py               KV cache: standalone study, then production via vLLM
├── 05_quantize.py               int8 / int4 quantization (GPTQ / AWQ / bitsandbytes)
├── 06_batching.py               continuous batching benchmark
├── 07_flash_attention.py        FlashAttention-2 enable + benchmark
├── 08_speculative_decoding.py   draft-model + target-model speculative decoding
├── 09_vllm_serve.py             production vLLM serving config
└── 10_benchmark.py              latency / throughput / cost / quality
```

Nothing is here yet. Read the plan doc first.
