package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/config"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/embed"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/escalation"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/inference"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/mcp"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/otto"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/rerank"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/retriever"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/watcher"
)

// Deps is the constructor argument for Orchestrator — all collaborators
// passed by interface so tests can mock them.
type Deps struct {
	Config    *config.Config
	Embedder  embed.Client
	Retriever retriever.Retriever
	Reranker  rerank.Client
	Inference inference.Client
	MCP       mcp.Client
	Otto      otto.Writer
	Logger    *slog.Logger
}

// Orchestrator processes CustomerMessage events from the watcher.
type Orchestrator struct {
	deps Deps

	// Cached tool lists per tenant (populated on first request).
	mu        sync.RWMutex
	toolCache map[string]toolCacheEntry
}

type toolCacheEntry struct {
	tools     []inference.Tool
	servers   map[string]mcp.ServerRef // function name → which MCP server hosts it
	cachedAt  time.Time
}

const (
	// Tunable retrieval/inference parameters. Kept here rather than in
	// config because they're not per-tenant — they describe the
	// platform's RAG pipeline shape.
	retrieveK    = 50
	rerankTopK   = 5
	historyLimit = 10
	toolCacheTTL = 5 * time.Minute
)

// New constructs an Orchestrator. Returns an error if Deps is missing
// required collaborators.
func New(d Deps) (*Orchestrator, error) {
	if d.Config == nil || d.Embedder == nil || d.Retriever == nil || d.Reranker == nil ||
		d.Inference == nil || d.MCP == nil || d.Otto == nil {
		return nil, errors.New("orchestrator: missing dependency")
	}
	if d.Logger == nil {
		return nil, errors.New("orchestrator: logger required")
	}
	return &Orchestrator{
		deps:      d,
		toolCache: map[string]toolCacheEntry{},
	}, nil
}

// Run drains events from in and processes them sequentially. Returns
// when ctx is cancelled. Each event runs with its own per-message
// timeout so a slow MCP server can't stall the pipeline.
func (o *Orchestrator) Run(ctx context.Context, in <-chan watcher.CustomerMessage) {
	o.deps.Logger.Info("orchestrator running")
	for {
		select {
		case <-ctx.Done():
			o.deps.Logger.Info("orchestrator stopping")
			return
		case ev, ok := <-in:
			if !ok {
				return
			}
			msgCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
			if err := o.processOne(msgCtx, ev); err != nil {
				o.deps.Logger.Error("turn failed",
					"err", err.Error(),
					"conversation_id", ev.ConversationID,
					"tenant_id", ev.TenantID,
				)
			}
			cancel()
		}
	}
}

func (o *Orchestrator) processOne(ctx context.Context, ev watcher.CustomerMessage) error {
	log := o.deps.Logger.With(
		"conversation_id", ev.ConversationID,
		"tenant_id", ev.TenantID,
		"message_id", ev.MessageID,
	)

	product, productName, ok := o.deps.Config.Routes.ResolveProduct(ev.TenantID)
	if !ok {
		// Unknown tenant and no default fallback — escalate so a human
		// notices the config gap.
		log.Warn("unknown tenant, escalating")
		return o.escalate(ctx, ev, "unknown_tenant")
	}
	log = log.With("product", productName)

	// Fire the per-tenant "chat started" hook the moment we see the
	// first customer message of a conversation. Lets the product's
	// own backend log the chat in its own systems — visibility
	// outside of Otto's admin inbox. Detection is by message count:
	// when only this one message exists, the conversation just
	// started. Best-effort — hook failures are logged but never
	// halt the orchestrator (the chat still gets answered).
	if product.ChatStartedHook.URL != "" {
		o.fireChatStartedHookIfFirst(ctx, ev, product)
	}

	evaluator := escalation.New(product.Escalation)

	// Pre-check: keywords and explicit human requests bypass the model.
	if d := evaluator.PreCheckCustomerMessage(ev.Body); d.Escalate {
		log.Info("pre-check escalation", "reason", d.Reason, "matched", d.Matched)
		return o.escalate(ctx, ev, string(d.Reason))
	}

	// 1. Embed the customer message.
	vecs, err := o.deps.Embedder.Embed(ctx, []string{ev.Body})
	if err != nil {
		log.Error("embedder failed", "err", err.Error())
		return o.escalate(ctx, ev, "embed_failed")
	}
	if len(vecs) == 0 {
		return o.escalate(ctx, ev, "embed_empty")
	}

	// 2. pgvector search.
	chunks, err := o.deps.Retriever.SearchNearest(ctx, product.RAGNamespace, vecs[0], retrieveK)
	if err != nil {
		log.Error("retrieval failed", "err", err.Error())
		// Retrieval failure isn't fatal — we can still ask the model
		// without RAG context. The system prompt's "say so honestly"
		// instruction handles the missing-context case.
		chunks = nil
	}

	// 3. Rerank if we got any chunks.
	topChunks := chunks
	if len(chunks) > 1 {
		docs := make([]string, len(chunks))
		for i, c := range chunks {
			docs[i] = c.Content
		}
		scores, rerr := o.deps.Reranker.Rerank(ctx, ev.Body, docs)
		if rerr == nil && len(scores) == len(chunks) {
			indices := rerank.TopK(scores, rerankTopK)
			topChunks = make([]retriever.Chunk, len(indices))
			for i, idx := range indices {
				topChunks[i] = chunks[idx]
			}
		} else {
			// Reranker failure: keep the retriever's nearest-K
			if len(chunks) > rerankTopK {
				topChunks = chunks[:rerankTopK]
			}
		}
	}

	// 4. Build prompt.
	systemPrompt, err := LoadSystemPrompt(product.SystemPromptFile)
	if err != nil {
		log.Warn("system prompt load failed, using default", "err", err.Error())
	}
	history, err := o.deps.Otto.RecentMessages(ctx, ev.ConversationID, historyLimit)
	if err != nil {
		log.Warn("history load failed", "err", err.Error())
	}
	customer, err := o.deps.Otto.Customer(ctx, ev.ConversationID)
	if err != nil {
		log.Warn("customer load failed, prompt will lack identity", "err", err.Error())
	}
	msgs := PromptBuilder{}.Build(systemPrompt, customer, topChunks, history, ev.Body)

	// 5. Discover tools for this tenant.
	tools, toolMap, err := o.resolveTools(ctx, ev.TenantID, product.MCPServers)
	if err != nil {
		log.Warn("tool discovery failed, proceeding without tools", "err", err.Error())
	}

	// 6. First inference call.
	resp, err := o.deps.Inference.Chat(ctx, inference.ChatRequest{
		Messages:    msgs,
		Tools:       tools,
		Temperature: 0.3,
		// MaxTokens caps the reply to ~2 sentences. Customer chat
		// surfaces work best when the assistant is crisp; longer
		// answers also blow past the per-slot context window on
		// follow-up turns.
		MaxTokens:   180,
	})
	if err != nil {
		log.Error("inference failed", "err", err.Error())
		return o.escalate(ctx, ev, "inference_failed")
	}

	choice := resp.Choices[0]
	toolFailures := 0

	// 7. Single round of tool calls (multi-round can come later).
	if len(choice.Message.ToolCalls) > 0 {
		msgs = append(msgs, choice.Message)
		for _, tc := range choice.Message.ToolCalls {
			server, knownTool := toolMap[tc.Function.Name]
			if !knownTool {
				toolFailures++
				msgs = append(msgs, inference.Message{
					Role:       inference.RoleTool,
					Name:       tc.Function.Name,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("error: tool %q not registered", tc.Function.Name),
				})
				continue
			}
			var args map[string]any
			if err := json.Unmarshal([]byte(tc.Function.Arguments), &args); err != nil {
				args = map[string]any{}
			}
			result, err := o.deps.MCP.Call(ctx, server, tc.Function.Name, args)
			if err != nil || result.IsError {
				toolFailures++
				msgs = append(msgs, inference.Message{
					Role:       inference.RoleTool,
					Name:       tc.Function.Name,
					ToolCallID: tc.ID,
					Content:    fmt.Sprintf("tool error: %v", err),
				})
				continue
			}
			msgs = append(msgs, inference.Message{
				Role:       inference.RoleTool,
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
				Content:    flattenToolContent(result),
			})
		}
		// Second inference call with tool results in scope.
		resp2, err := o.deps.Inference.Chat(ctx, inference.ChatRequest{
			Messages:    msgs,
			Tools:       tools,
			Temperature: 0.3,
			MaxTokens:   400,
		})
		if err != nil {
			log.Error("second inference call failed", "err", err.Error())
			return o.escalate(ctx, ev, "inference_failed")
		}
		choice = resp2.Choices[0]
	}

	reply := choice.Message.Content
	confidence := scoreConfidence(reply, choice.FinishReason)

	// Count the customer's turns (prior history + this message) so the
	// policy can give the bot a few exchanges before handing a
	// low-confidence conversation to a human.
	customerTurns := 1
	for _, h := range history {
		if h.SenderType == "customer" {
			customerTurns++
		}
	}

	d := evaluator.CheckPostInference(confidence, toolFailures, customerTurns)
	if d.Escalate {
		log.Info("post-check escalation", "reason", d.Reason, "confidence", confidence, "tool_failures", toolFailures, "customer_turns", customerTurns)
		return o.escalate(ctx, ev, string(d.Reason))
	}

	if reply == "" {
		log.Warn("empty model reply, escalating")
		return o.escalate(ctx, ev, "empty_reply")
	}

	// Low confidence but still inside the try-first window: keep helping,
	// and append a soft offer so the customer knows a human/vendor
	// handoff is one ask away (saying "talk to a human" forces it via
	// the pre-check on their next message).
	if d.OfferHandoff {
		reply += "\n\n" + handoffOfferText
		log.Info("offering soft handoff", "confidence", confidence, "customer_turns", customerTurns)
	}

	if err := o.deps.Otto.PostAssistantMessage(ctx, otto.AssistantMessage{
		ConversationID: ev.ConversationID,
		TenantID:       ev.TenantID,
		StoreID:        ev.StoreID,
		Body:           reply,
		SenderID:       "qwen2.5-1.5b-instruct",
		SenderName:     "Otto AI",
	}); err != nil {
		return fmt.Errorf("post assistant message: %w", err)
	}
	log.Info("assistant reply posted", "confidence", confidence, "len", len(reply), "offered_handoff", d.OfferHandoff)
	return nil
}

// handoffOfferText is appended to a low-confidence reply while the bot is
// still within its try-first window. It tells the customer how to force a
// human handoff ("talk to a human" is a humanRequestPhrase the pre-check
// matches on the next turn).
const handoffOfferText = "If this didn't fully resolve your question, I can connect you to the vendor or a human agent — just reply \"talk to a human\" and I'll hand you over."

func (o *Orchestrator) escalate(ctx context.Context, ev watcher.CustomerMessage, reason string) error {
	if err := o.deps.Otto.MarkNeedsHuman(ctx, ev.ConversationID, reason); err != nil {
		return fmt.Errorf("mark needs_human: %w", err)
	}
	// Soft hand-off message so the customer isn't left in silence.
	_ = o.deps.Otto.PostAssistantMessage(ctx, otto.AssistantMessage{
		ConversationID: ev.ConversationID,
		TenantID:       ev.TenantID,
		StoreID:        ev.StoreID,
		Body:           "I'm connecting you to a human agent who can help with this. They'll be with you shortly.",
		SenderID:       "slm-router",
		SenderName:     "Otto",
	})
	// Optional per-tenant escalation hook — POSTs the conversation
	// context to the product's own ticket service so a durable
	// ticket lands in the merchant's dashboard immediately. Failure
	// here is logged but doesn't fail the escalation: needs_human
	// is already set so the Otto admin inbox will still surface it.
	if product, _, ok := o.deps.Config.Routes.ResolveProduct(ev.TenantID); ok {
		o.fireEscalationHook(ctx, ev, reason, product)
	}
	return nil
}

// fireChatStartedHookIfFirst fires the per-tenant "new chat started"
// hook when ev is the first customer message in its conversation.
// Wrapper around fireHook with `escalation_reason` deliberately
// omitted (the merchant gets the same context shape as an escalation
// but without the human-handoff signal — this is purely a log/notify
// event). Detection: count messages in the conversation; if exactly
// 1, this insert IS that one. The recent-history fetch is best-
// effort — on failure we still fire the hook with empty history.
func (o *Orchestrator) fireChatStartedHookIfFirst(
	ctx context.Context, ev watcher.CustomerMessage, product config.ProductConfig,
) {
	history, _ := o.deps.Otto.RecentMessages(ctx, ev.ConversationID, 2)
	if len(history) > 1 {
		return // not the first message — likely a reconnect or a follow-up
	}
	o.fireHook(ctx, "chat_started", ev, product.ChatStartedHook, "")
}

func (o *Orchestrator) fireEscalationHook(
	ctx context.Context, ev watcher.CustomerMessage, reason string, product config.ProductConfig,
) {
	o.fireHook(ctx, "escalation", ev, product.EscalationHook, reason)
}

// fireHook is the common POST machinery shared between
// fireEscalationHook and fireChatStartedHookIfFirst. The two hook
// shapes are deliberately identical (same JSON keys, same auth) so
// product endpoints can share parsing — they differ only in whether
// `escalation_reason` is set.
func (o *Orchestrator) fireHook(
	ctx context.Context,
	kind string,
	ev watcher.CustomerMessage,
	hook config.EscalationHook,
	escalationReason string,
) {
	if hook.URL == "" {
		return
	}
	log := slog.With(
		"service", "slm-router",
		"hook_kind", kind,
		"conversation_id", ev.ConversationID,
		"tenant_id", ev.TenantID,
		"hook_url", hook.URL,
	)
	// Reuse the same payload-building helpers the escalation hook
	// uses so the merchant gets a consistent shape across both
	// hook kinds. Both fetches are best-effort.
	customer, _ := o.deps.Otto.Customer(ctx, ev.ConversationID)
	history, _ := o.deps.Otto.RecentMessages(ctx, ev.ConversationID, historyLimit)
	subject := truncate(ev.Body, 280)
	description := renderTranscript(history, ev.Body)

	payload := map[string]any{
		"conversation_id": ev.ConversationID,
		"tenant_id":       ev.TenantID,
		"store_id":        ev.StoreID,
		"customer_name":   customer.Name,
		"customer_email":  customer.Email,
		"subject":         subject,
		"description":     description,
	}
	if escalationReason != "" {
		payload["escalation_reason"] = escalationReason
	}
	body, err := json.Marshal(payload)
	if err != nil {
		log.Warn("hook marshal failed", "err", err.Error())
		return
	}
	hookCtx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(hookCtx, http.MethodPost, hook.URL, bytes.NewReader(body))
	if err != nil {
		log.Warn("hook build failed", "err", err.Error())
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if hook.AuthHeader != "" && hook.AuthEnvVar != "" {
		if secret := os.Getenv(hook.AuthEnvVar); secret != "" {
			req.Header.Set(hook.AuthHeader, secret)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Warn("hook POST failed", "err", err.Error())
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		log.Warn("hook rejected", "status", res.StatusCode)
		return
	}
	log.Info("hook fired", "status", res.StatusCode)
}

func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

func renderTranscript(history []otto.HistoryMessage, lastCustomerMsg string) string {
	var b strings.Builder
	for _, h := range history {
		who := h.SenderType
		switch who {
		case "customer":
			who = "Customer"
		case "assistant", "staff":
			who = "Otto AI"
		}
		fmt.Fprintf(&b, "[%s] %s\n", who, h.Body)
	}
	if lastCustomerMsg != "" {
		fmt.Fprintf(&b, "[Customer] %s\n", lastCustomerMsg)
	}
	return b.String()
}

func (o *Orchestrator) resolveTools(ctx context.Context, tenantID string, servers []config.MCPServerConfig) ([]inference.Tool, map[string]mcp.ServerRef, error) {
	o.mu.RLock()
	entry, ok := o.toolCache[tenantID]
	o.mu.RUnlock()
	if ok && time.Since(entry.cachedAt) < toolCacheTTL {
		return entry.tools, entry.servers, nil
	}

	var tools []inference.Tool
	serverMap := map[string]mcp.ServerRef{}
	for _, s := range servers {
		ref := mcp.ServerRef{Name: s.Name, URL: s.URL, AuthHeader: s.AuthHeader, AuthEnvVar: s.AuthEnvVar}
		discovered, err := o.deps.MCP.ListTools(ctx, ref)
		if err != nil {
			return nil, nil, fmt.Errorf("list tools on %s: %w", s.Name, err)
		}
		for _, t := range discovered {
			tools = append(tools, inference.Tool{
				Type: "function",
				Function: inference.ToolFunction{
					Name:        t.Name,
					Description: t.Description,
					Parameters:  t.InputSchema,
				},
			})
			serverMap[t.Name] = ref
		}
	}
	o.mu.Lock()
	o.toolCache[tenantID] = toolCacheEntry{tools: tools, servers: serverMap, cachedAt: time.Now()}
	o.mu.Unlock()
	return tools, serverMap, nil
}

func flattenToolContent(r mcp.CallResult) string {
	var b strings.Builder
	first := true
	for _, blk := range r.Content {
		if blk.Type != "text" || blk.Text == "" {
			continue
		}
		if !first {
			b.WriteByte('\n')
		}
		b.WriteString(blk.Text)
		first = false
	}
	return b.String()
}
