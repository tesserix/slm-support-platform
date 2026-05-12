# Phase 2 — Fine-tuning an open SLM

Placeholder. We get here after Phase 1 is done and the fundamentals are solid.

## Plan (subject to refinement once Phase 1 ships)

- Pick a base model: Phi-3-mini (3.8B), Llama-3.2-3B, or Gemma-2-2B
- Assemble Tesserix-domain training data: product docs, anonymised support transcripts, FAQ pairs
- LoRA / QLoRA fine-tune on a single GPU (full fine-tune is overkill at this scale)
- Evaluate against a held-out set of real support questions
- Quantize for serving (int8 or int4) and benchmark latency
