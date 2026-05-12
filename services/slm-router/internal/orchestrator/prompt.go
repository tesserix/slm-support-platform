// Package orchestrator wires the per-customer-message agent loop:
// pre-check escalation, embed + retrieve + rerank, call inference
// (with one round of MCP tool calls if requested), post-check escalation,
// and post the result back to Otto.
package orchestrator

import (
	"fmt"
	"os"
	"strings"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/inference"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/otto"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/retriever"
)

// PromptBuilder composes the message list sent to the inference service.
// Stateless — every call produces a fresh slice.
type PromptBuilder struct{}

// Build assembles: system prompt, retrieved RAG chunks, conversation
// history (oldest first), and the new customer message. Returns the
// messages array ready for inference.Client.Chat.
func (PromptBuilder) Build(systemPrompt string, chunks []retriever.Chunk, history []otto.HistoryMessage, customerMessage string) []inference.Message {
	msgs := make([]inference.Message, 0, 2+len(history))

	// Compose the system prompt with retrieved context appended. Keep
	// the chunks clearly labelled so the model knows what's RAG vs
	// what's its baked-in knowledge.
	var b strings.Builder
	b.WriteString(systemPrompt)
	if len(chunks) > 0 {
		b.WriteString("\n\nRelevant context from product documentation:\n")
		for i, c := range chunks {
			fmt.Fprintf(&b, "\n[%d] %s\n", i+1, c.Content)
		}
		b.WriteString("\nUse this context where relevant. If the context does not contain the answer, say so honestly rather than guessing.")
	}
	msgs = append(msgs, inference.Message{Role: inference.RoleSystem, Content: b.String()})

	// History — convert Otto senders to inference roles.
	for _, h := range history {
		role := historyRole(h.SenderType)
		if role == "" {
			continue // skip system messages from Otto itself
		}
		msgs = append(msgs, inference.Message{Role: role, Content: h.Body})
	}

	// The new customer message at the end. (Note: history may already
	// include it depending on whether the change stream delivers the
	// message before our history fetch. PromptBuilder doesn't try to
	// deduplicate — that's the orchestrator's job.)
	msgs = append(msgs, inference.Message{Role: inference.RoleUser, Content: customerMessage})
	return msgs
}

func historyRole(senderType string) inference.Role {
	switch senderType {
	case "customer":
		return inference.RoleUser
	case "staff", "assistant":
		return inference.RoleAssistant
	default:
		return ""
	}
}

// LoadSystemPrompt reads a system prompt from disk and returns its
// contents. Returns a sensible default if the file is missing (we never
// want a missing prompt file to take the orchestrator down — the
// readiness probe handles the boot-time check).
func LoadSystemPrompt(path string) (string, error) {
	if path == "" {
		return defaultSystemPrompt, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return defaultSystemPrompt, nil
		}
		return "", err
	}
	return string(b), nil
}

const defaultSystemPrompt = `You are a helpful customer support assistant for a Tesserix product. Be concise, warm, and action-oriented. If you don't know the answer or cannot help, say so directly and offer to escalate to a human agent. Never invent product details, prices, or policies you don't have explicit context for.`
