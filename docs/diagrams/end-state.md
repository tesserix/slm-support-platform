# Diagram — End-state production system

Standalone copy of the end-state Mermaid diagram for easy reference. Source of truth is [`../02-end-state-architecture.md`](../02-end-state-architecture.md).

```mermaid
graph TB
    subgraph Products
        M[mark8ly]
        F[fanzone]
        H[HomeChef]
        G[gameverse]
        S[stockpilot]
    end

    subgraph Edge
        CW[Chat widget]
    end

    subgraph GKE
        BFF[support-bff]
        ROUTER[Router agent]
        REG[Customer AI Registry]
        RAG[RAG retriever]
        VDB[Vector DB]
        SLM[SLM inference]
        RUNTIME[Tesserix MCP Runtime<br/>stateless 2026-07-28]
        TOOLS[Per-product tool catalogs]
    end

    subgraph ProductAPIs
        MAPI[mark8ly API]
        FAPI[fanzone API]
        HAPI[homechef API]
    end

    M --> CW
    F --> CW
    H --> CW
    G --> CW
    S --> CW
    CW --> BFF
    BFF --> ROUTER
    ROUTER --> RAG
    RAG --> VDB
    ROUTER --> SLM
    ROUTER -->|resolve approved manifest| REG
    REG -->|qualified immutable version| RUNTIME
    RAG --> SLM
    SLM --> ROUTER
    ROUTER -->|independent list/call| RUNTIME
    RUNTIME --> TOOLS
    TOOLS --> MAPI
    TOOLS --> FAPI
    TOOLS --> HAPI
    SLM --> BFF
    BFF --> CW
```

## What each component is

- **Products** — your existing frontends. The chat widget is loaded by each one.
- **Chat widget** — small JS bundle, posts user messages to the BFF over HTTPS, streams tokens back.
- **support-bff** — Go/Gin service. Validates session, attaches product context, orchestrates the agent loop.
- **Router agent** — detects which product the user is on and what kind of question it is; picks the RAG namespace and prompt.
- **RAG retriever + Vector DB** — per-product namespaces (mark8ly, fanzone, homechef, …) holding embedded product docs.
- **SLM inference** — vLLM-served fine-tuned Phi-3-mini (or equivalent), runs on a single L4 GPU in-cluster.
- **Customer AI Registry** — the Tesserix-owned registry of qualified MCP
  manifests and immutable versions; Solo.io's registry is not in the runtime path.
- **Tesserix MCP Runtime** — authenticates and bounds every stateless MCP request,
  rejects session affinity, exports health/metrics, and hosts each product catalog.
- **Per-product tool catalogs** — bridge approved model tool calls to product APIs.
- **Product APIs** — the existing per-product backends (homechef-api, mark8ly APIs, etc.).

## Request flow walkthrough (a HomeChef customer asks about their order)

1. Customer on `fe3dr.com` opens the chat widget, types "where's my order?"
2. Widget posts `{message, session_token, product="homechef"}` to `support-bff`
3. BFF validates session via existing HomeChef auth-BFF, attaches `user_id`
4. Router classifies: product=homechef, intent=order-status, needs a tool
5. RAG retrieves `homechef/customer-faq` chunks about order tracking (fallback if the tool fails)
6. SLM receives system prompt plus RAG context plus user message, emits a tool call `lookup_order(user_id=...)`
7. Tool layer calls homechef-api `GET /orders/latest?user_id=...`, gets back order details
8. SLM receives tool result, generates a natural-language reply: "Your order #12345 is out for delivery, ETA 14:30"
9. Tokens stream back through BFF to the widget to the customer
