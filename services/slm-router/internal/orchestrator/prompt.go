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
//
// The customer identity (user_id / email / name) is appended to the
// system prompt as a "Customer:" block. Tools like get_user_points
// take a user_id argument and the SLM MUST pass the customer's REAL
// id, not invent one — otherwise the tool returns someone else's
// data or 404s. Putting the id at the top of the prompt grounds the
// model on the right value.
func (PromptBuilder) Build(systemPrompt string, customer otto.CustomerIdentity, chunks []retriever.Chunk, history []otto.HistoryMessage, customerMessage string) []inference.Message {
	msgs := make([]inference.Message, 0, 2+len(history))

	// Compose the system prompt with retrieved context appended. Keep
	// the chunks clearly labelled so the model knows what's RAG vs
	// what's its baked-in knowledge.
	var b strings.Builder
	b.WriteString(systemPrompt)
	// Universal brevity directive — every tenant inherits this. The
	// chat surface is a 320px-wide widget; long answers wrap badly,
	// and the model also tends to hallucinate when given room to
	// ramble. When a tool returns real numbers, quote them VERBATIM
	// in one short sentence rather than restating them.
	b.WriteString(
		"\n\nVoice: write like a capable colleague, not a manual. Contractions, " +
			"plain words, no corporate filler. You may open with a short human " +
			"beat when the customer is frustrated or out of pocket (\"Ah, that's " +
			"annoying —\"), but at most one clause, and never on a neutral " +
			"question. Never say \"I apologise for the inconvenience\", \"kindly\", " +
			"\"please be informed\", or \"as per our policy\".\n" +
			"\nNavigation (this is what makes an answer useful):\n" +
			"- When the answer involves doing something in the app, give the exact " +
			"tap-path in bold arrow form, e.g. \"More → Payout\". Name the screen " +
			"the customer will actually see.\n" +
			"- Use ONLY paths that appear in the context below. If the context has " +
			"no path for what they asked, say what you do know and offer a human — " +
			"never guess a screen name, and never invent a plausible-sounding one.\n" +
			"- If two screens are involved, name both and say which does what.\n" +
			"\nResponse rules (apply to EVERY reply):\n" +
			"- DEFAULT length: at most 2 short sentences (~40 words total), plus " +
			"the tap-path. No sign-off (\"Hope this helps\").\n" +
			"- EXCEPTION: when the customer explicitly asks for a " +
			"breakdown, list, history, daily/weekly summary, comparison, or " +
			"step-by-step explanation, you MAY use a short bullet list or a " +
			"compact table-style block. Keep each bullet to one line and " +
			"cap the whole reply at ~120 words. Still no preamble or sign-off.\n" +
			"- If a tool returned a number, name, status, or any concrete value, " +
			"quote it verbatim. Do not paraphrase or round. For a per-day " +
			"breakdown, list the dates exactly as returned by the tool.\n" +
			"- If a tool returned `error: range_exceeded`, follow its " +
			"`_action_for_assistant` instruction verbatim — do NOT fabricate " +
			"values for the requested window.\n" +
			"- If a tool returned `error: not_implemented`, say exactly: " +
			"\"I can't fetch that yet — would you like me to connect you to a human?\".\n" +
			"- If a tool returned `error: backend_unreachable` or any other " +
			"error, say: \"I'm having trouble reaching that data right now — " +
			"would you like me to connect you to a human?\".\n" +
			"- Never invent values, never guess.\n",
	)
	if customer.UserID != "" || customer.Email != "" {
		b.WriteString("\n\nCustomer (use these EXACT values for any tool argument named user_id, customer_id, or email — never invent or guess them):\n")
		if customer.UserID != "" {
			fmt.Fprintf(&b, "- user_id: %s\n", customer.UserID)
		}
		if customer.Email != "" {
			fmt.Fprintf(&b, "- email: %s\n", customer.Email)
		}
		if customer.Name != "" {
			fmt.Fprintf(&b, "- name: %s\n", customer.Name)
		}
	}
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
