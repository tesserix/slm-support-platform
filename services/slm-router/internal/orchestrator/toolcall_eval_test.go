package orchestrator

// toolcall_eval_test.go — does the 1.5B model actually CALL the tools it has?
//
// Everything else about the agent loop is unit-tested against a mock inference
// client, which proves the plumbing and nothing about the model. Tool selection
// is the one behaviour that cannot be asserted without the real weights: a 1.5B
// model handed eleven overlapping schemas may answer from memory, invent a tool,
// or pick the wrong one of two that do the same job — and every one of those
// looks like a healthy conversation in the logs.
//
// So this is an opt-in integration eval, not a CI unit test. It talks to a live
// slm-inference (and optionally a live mcp-gateway for the real schemas) and
// reports a PASS RATE, because reliability is the question. A single sample at
// temperature 0.3 tells you nothing; the same prompt is replayed N times.
//
//	kubectl port-forward -n support-platform svc/support-platform-slm-inference 8000:8000
//	kubectl port-forward -n homechef svc/homechef-mcp 8766:8765
//
//	OTTO_EVAL_INFERENCE_URL=http://127.0.0.1:8000 \
//	OTTO_EVAL_MCP_URL=http://127.0.0.1:8766/mcp \
//	go test ./internal/orchestrator/ -run TestToolCallReliability -v -timeout 30m
//
// Knobs: OTTO_EVAL_REPEATS (default 5), OTTO_EVAL_MODEL, OTTO_EVAL_MIN_RATE
// (default 0.8), OTTO_EVAL_TOOLSET (all|deduped, default all).

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tesserix/slm-support-platform/services/slm-router/internal/inference"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/mcp"
	"github.com/tesserix/slm-support-platform/services/slm-router/internal/otto"
)

// evalCase is one customer message and the tool set that would be a correct
// response to it. wantTools holds every ACCEPTABLE choice, not one right
// answer: where the gateway exposes two tools for the same job, calling either
// is correct behaviour by the model and a duplication problem by the gateway.
// wantNoTool marks the messages that must be answered from context alone —
// calling a tool there burns a round-trip and often invents an id.
type evalCase struct {
	name       string
	message    string
	wantTools  []string
	wantNoTool bool
}

// evalCases cover the four intents the homechef assistant actually receives,
// plus the two negative cases that catch an over-eager caller.
var evalCases = []evalCase{
	{
		name:      "order status by id",
		message:   "What's happening with my order HC-4821? It's been over an hour.",
		wantTools: []string{"get_order_status", "autoGetOrder"},
	},
	{
		name:      "delivery tracking",
		message:   "Where is my delivery for order HC-4821 right now?",
		wantTools: []string{"track_delivery", "autoTrackDelivery", "get_order_status", "autoGetOrder"},
	},
	{
		name:      "recent order history",
		message:   "Can you show me the orders I placed in the last week?",
		wantTools: []string{"list_recent_orders", "autoListRecentOrders"},
	},
	{
		name:      "chef availability",
		message:   "Is chef 7c9e6679-7425-40de-944b-e07fc1f90ae7 taking orders today?",
		wantTools: []string{"get_chef_availability", "autoGetChefAvailability"},
	},
	{
		name:      "refund request",
		message:   "Order HC-4821 arrived cold and inedible. I want a refund.",
		wantTools: []string{"create_refund_request", "get_order_status", "autoGetOrder"},
	},
	{
		name:      "policy question answered from context",
		message:   "How long do refunds usually take to reach my bank?",
		wantTools: []string{"search_knowledge_base"},
	},
	{
		name:       "greeting needs no tool",
		message:    "hi there",
		wantNoTool: true,
	},
	{
		name:       "thanks needs no tool",
		message:    "great, thanks for the help!",
		wantNoTool: true,
	},
}

// dedupedToolset is the hand-rolled set with the auto-generated OpenAPI twins
// removed. Selecting it with OTTO_EVAL_TOOLSET=deduped measures what pruning
// the overlap is worth, so the decision to prune rests on a number.
var autoTwins = map[string]bool{
	"autoGetOrder":            true,
	"autoTrackDelivery":       true,
	"autoGetChefAvailability": true,
	"autoListRecentOrders":    true,
}

func TestToolCallReliability(t *testing.T) {
	inferenceURL := os.Getenv("OTTO_EVAL_INFERENCE_URL")
	if inferenceURL == "" {
		t.Skip("set OTTO_EVAL_INFERENCE_URL to run the tool-calling eval (needs live slm-inference)")
	}

	repeats := envInt("OTTO_EVAL_REPEATS", 5)
	minRate := envFloat("OTTO_EVAL_MIN_RATE", 0.8)

	tools := loadEvalTools(t)
	if os.Getenv("OTTO_EVAL_TOOLSET") == "deduped" {
		tools = pruneAutoTwins(tools)
	}
	if len(tools) == 0 {
		t.Fatal("no tools resolved — the eval cannot say anything about tool calling")
	}
	t.Logf("tool set (%d): %s", len(tools), strings.Join(toolNames(tools), ", "))

	client := inference.NewHTTP(inferenceURL,
		inference.WithTimeout(90*time.Second),
		inference.WithModel(envStr("OTTO_EVAL_MODEL", "qwen2.5-1.5b-instruct")),
	)

	// The eval must see the SAME prompt production sends, or it measures a
	// prompt nobody ships — including the tool directive the orchestrator
	// appends whenever tools were discovered, which is the whole difference
	// between a tool call and a sentence about one.
	systemPrompt := loadSystemPrompt(t)
	if os.Getenv("OTTO_EVAL_NO_TOOL_DIRECTIVE") == "" {
		systemPrompt += ToolUseDirective
	}
	customer := otto.CustomerIdentity{
		UserID: "7c9e6679-7425-40de-944b-e07fc1f90ae7",
		Email:  "eval.customer@example.com",
		Name:   "Eval Customer",
	}

	var totalOK, totalRuns int
	results := make([]string, 0, len(evalCases))

	for _, tc := range evalCases {
		t.Run(tc.name, func(t *testing.T) {
			var ok int
			picked := map[string]int{}

			for i := range repeats {
				msgs := PromptBuilder{}.Build(systemPrompt, customer, nil, nil, tc.message)

				ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
				resp, err := client.Chat(ctx, inference.ChatRequest{
					Messages:    msgs,
					Tools:       tools,
					Temperature: 0.3, // the production value — measure what ships
					MaxTokens:   180,
				})
				cancel()
				if err != nil {
					t.Fatalf("run %d: inference failed: %v", i+1, err)
				}
				if len(resp.Choices) == 0 {
					t.Fatalf("run %d: model returned no choices", i+1)
				}

				calls := resp.Choices[0].Message.ToolCalls
				switch {
				case tc.wantNoTool:
					if len(calls) == 0 {
						ok++
					} else {
						picked[calls[0].Function.Name]++
					}
				case len(calls) == 0:
					picked["<no tool: answered from memory>"]++
				default:
					name := calls[0].Function.Name
					picked[name]++
					if slicesContains(tc.wantTools, name) && argsParse(calls[0].Function.Arguments) {
						ok++
					}
				}
			}

			rate := float64(ok) / float64(repeats)
			totalOK += ok
			totalRuns += repeats
			results = append(results, fmt.Sprintf("  %-42s %3.0f%%  %s",
				tc.name, rate*100, formatPicked(picked)))

			if rate < minRate {
				t.Errorf("tool-call rate %.0f%% is below the %.0f%% floor (wanted one of %v); model picked %s",
					rate*100, minRate*100, tc.wantTools, formatPicked(picked))
			}
		})
	}

	sort.Strings(results)
	t.Logf("\ntool-call reliability over %d runs/case:\n%s\n  OVERALL %.0f%% (%d/%d)",
		repeats, strings.Join(results, "\n"), float64(totalOK)/float64(totalRuns)*100, totalOK, totalRuns)
}

// TestToolSchemasAreDistinct is a pure unit test — no model, no network. It
// fails when the gateway exposes two tools the model cannot choose between,
// which is the failure that makes the eval above flaky in the first place.
func TestToolSchemasAreDistinct(t *testing.T) {
	if os.Getenv("OTTO_EVAL_MCP_URL") == "" {
		t.Skip("set OTTO_EVAL_MCP_URL to check the live tool set for duplicates")
	}
	tools := loadEvalTools(t)

	// Group by the (sorted required-arg list + one-line intent) fingerprint a
	// small model actually discriminates on.
	bySignature := map[string][]string{}
	for _, tool := range tools {
		req := requiredArgs(tool.Function.Parameters)
		sort.Strings(req)
		sig := strings.Join(req, ",")
		bySignature[sig] = append(bySignature[sig], tool.Function.Name)
	}

	for sig, names := range bySignature {
		if len(names) > 1 {
			sort.Strings(names)
			t.Errorf("tools %v share the required-argument signature (%s) — a 1.5B model "+
				"cannot reliably choose between them; expose one", names, sig)
		}
	}
}

func loadEvalTools(t *testing.T) []inference.Tool {
	t.Helper()
	mcpURL := os.Getenv("OTTO_EVAL_MCP_URL")
	if mcpURL == "" {
		t.Skip("set OTTO_EVAL_MCP_URL to the mcp-gateway /mcp endpoint")
	}

	client := mcp.NewHTTP()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	discovered, err := client.ListTools(ctx,
		mcp.ServerRef{Name: "eval", URL: mcpURL},
		mcp.CallContext{TenantID: envStr("OTTO_EVAL_TENANT", "homechef")},
	)
	if err != nil {
		t.Fatalf("tools/list against %s failed: %v", mcpURL, err)
	}

	tools := make([]inference.Tool, 0, len(discovered))
	for _, d := range discovered {
		tools = append(tools, inference.Tool{
			Type: "function",
			Function: inference.ToolFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  d.InputSchema,
			},
		})
	}
	return tools
}

// loadSystemPrompt reads the tenant prompt the deployment mounts, falling back
// to a minimal stand-in so the eval still runs off-cluster.
func loadSystemPrompt(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("OTTO_EVAL_SYSTEM_PROMPT_FILE"); p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read system prompt %s: %v", p, err)
		}
		return string(b)
	}
	return "You are Otto, the support assistant for Fe3dr, a home-chef food " +
		"delivery service. Answer briefly and never invent an order id."
}

func pruneAutoTwins(tools []inference.Tool) []inference.Tool {
	out := tools[:0:0]
	for _, tool := range tools {
		if !autoTwins[tool.Function.Name] {
			out = append(out, tool)
		}
	}
	return out
}

func requiredArgs(schema map[string]any) []string {
	raw, ok := schema["required"].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func toolNames(tools []inference.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Function.Name)
	}
	return out
}

// argsParse reports whether the model emitted syntactically valid JSON
// arguments. A tool call with a malformed body is a failed call — the
// orchestrator unmarshals it into an empty map and calls the tool blind.
func argsParse(args string) bool {
	if strings.TrimSpace(args) == "" {
		return true // no-argument tools legitimately send nothing
	}
	var v map[string]any
	return json.Unmarshal([]byte(args), &v) == nil
}

func formatPicked(picked map[string]int) string {
	if len(picked) == 0 {
		return "(as expected)"
	}
	keys := make([]string, 0, len(picked))
	for k := range picked {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s×%d", k, picked[k]))
	}
	return strings.Join(parts, " ")
}

func slicesContains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil && v > 0 {
		return v
	}
	return def
}
