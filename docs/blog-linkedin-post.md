# LinkedIn post — promote the SLM blog

Copy everything below the dividing line into LinkedIn's post composer.

Recommended attachment: rendered preview of `https://blog.tesserix.app/small-language-models-and-why-we-built-one` (LinkedIn auto-fetches the OG card, no need to upload an image).

Sweet spot: ~1,800 characters incl. hashtags. Tested on a 4-line "above-the-fold" hook.

---

🧠 We didn't build our own Small Language Model because it was trendy.

We built one because the frontier-model workflow was heavier than the product decisions we needed to make over and over. Most of our prompts are narrow, structured, and product-specific. A smaller model, fine-tuned for our domain and run close to the product, fit the work better.

This is the thinking behind Domain Driven SLM 👇

🎯 WHAT IT IS
A small language model — for us, roughly 1B–4B parameters — fine-tuned for our specific products. Self-hosted, inside our own cluster. Note: "small" isn't a hard cutoff; many teams call anything from a few hundred million up to ~7–10B parameters an SLM.

🏗️ HOW IT WORKS (one model, three "personalities")
✔ Otto — multi-tenant chat front-of-house
✔ slm-router — the gateway. Picks system prompt + RAG namespace + MCP tool set per tenant
✔ RAG layer — embedder, reranker, pgvector with per-product namespaces
✔ MCP servers — per-product safe tool surface (lookup_order, get_payout, etc.)

Same brain. Three desks. The HomeChef desk knows payouts. The mark8ly desk knows orders. The fanzone desk knows rewards. The model never sees the other two desks.

✅ WHERE IT FITS
• Narrow, well-scoped tasks where a fine-tuned SLM can be more predictable than a general-purpose LLM
• Customer support chat across multiple products
• "Where's my order?" / "How do I claim X?" / FAQ-shaped queries
• Flows where you'd rather not have data leave your environment — running locally improves privacy, though it doesn't automatically guarantee it

❌ WHAT NOT TO EXPECT
• It is not GPT-4. It won't write you a sonnet about French monetary policy.
• Long multi-step reasoning — keep agent loops short, use tools for state
• Rare or recent knowledge from pretraining — that's RAG's job, not the weights'
• Hallucination-free output — smaller + narrower can reduce certain failure modes, but the model can still get things wrong. Escalate refunds, complaints, anything risky to a human.

💡 WHY NOW (the product vision)
Our products are still in incubation, so we are not chasing today's bill — we are shaping the curve of it. Hosted LLMs scale roughly linearly with adoption; an SLM gives us a much gentler curve when one of these products starts to take off.

The same weights can run on a phone, a Raspberry Pi, a single GPU, or just a CPU server — useful flexibility for edge inference, privacy-sensitive flows, and on-prem deployments later.

📖 Full deep-dive (≈10 min read, plain English, with system + customer-flow diagrams):
👉 https://blog.tesserix.app/small-language-models-and-why-we-built-one

What would you put a domain-specific SLM to work on first — support, internal tools, or something else? Curious what other teams are seeing.

— Samyak Rout
Founder, Tesserix

#SLM #GenAI #LLM #RAG #MCP #MLOps #AIInfrastructure #PlatformEngineering #DevOps #Kubernetes #CloudNative #ProductEngineering #BuildInPublic
