# Diagram — End-state production system

Standalone copy of the end-state Mermaid diagram for easy reference. Source of truth is [`../02-end-state-architecture.md`](../02-end-state-architecture.md).

```mermaid
flowchart TB
    subgraph "Products (frontends)"
        M[mark8ly.com<br/>storefront]
        F[fanzone-battleground.com]
        H[fe3dr.com<br/>HomeChef]
        G[gameverse.tesserix.com]
        S[stockpilot.tesserix.com]
    end

    subgraph "Edge"
        CW[Chat widget<br/>iframe / web component]
    end

    subgraph "GKE: support-platform namespace"
        BFF[support-bff<br/>Go / Gin]
        ROUTER[Router agent<br/>detects product + intent]
        RAG[RAG retriever<br/>per-product namespaces]
        VDB[(Vector DB<br/>Qdrant / pgvector)]
        SLM[SLM inference<br/>vLLM serving Phi-3-mini<br/>fine-tuned on Tesserix data]
        TOOLS[Tool layer<br/>order lookup, ticket create,<br/>escalation, refund-status]
    end

    subgraph "Existing product APIs"
        MAPI[mark8ly APIs]
        FAPI[fanzone APIs]
        HAPI[homechef APIs]
    end

    M --> CW
    F --> CW
    H --> CW
    G --> CW
    S --> CW

    CW -->|HTTPS + session| BFF
    BFF --> ROUTER
    ROUTER -->|product=mark8ly| RAG
    RAG --> VDB
    ROUTER --> SLM
    RAG --> SLM
    SLM -->|wants to call tool| TOOLS
    TOOLS --> MAPI
    TOOLS --> FAPI
    TOOLS --> HAPI
    SLM -->|streamed tokens| BFF
    BFF -->|SSE / WebSocket| CW
```

## Request flow walkthrough (a HomeChef customer asks about their order)

1. Customer on `fe3dr.com` opens the chat widget, types "where's my order?"
2. Widget posts `{message, session_token, product="homechef"}` to `support-bff`
3. BFF validates session via existing HomeChef auth-BFF, attaches `user_id`
4. Router classifies: product=homechef, intent=order-status → needs tool
5. RAG retrieves `homechef/customer-faq` chunks about order tracking (in case the tool fails)
6. SLM receives: system prompt + RAG context + user message → emits a tool call: `lookup_order(user_id=...)`
7. Tool layer calls homechef-api `GET /orders/latest?user_id=...`, gets back order details
8. SLM receives tool result, generates natural-language response: "Your order #12345 is out for delivery, ETA 14:30"
9. Tokens stream back through BFF → widget → customer
