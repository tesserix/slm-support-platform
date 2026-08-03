// Package inference talks to the slm-inference service over HTTP.
//
// We assume an OpenAI-compatible chat-completions endpoint (the default
// provided by both llama.cpp's server mode and vLLM's OpenAI-compat
// shim). Sticking to this contract means we can swap the backend
// without touching the orchestrator.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is the orchestrator-facing surface. Mockable for tests.
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
}

// Role is the OpenAI role enum.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in the conversation history sent to the model.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	Name       string     `json:"name,omitempty"`
}

// Tool describes a function the model can call. Mirrors the OpenAI
// tool-calling spec. The JSON-schema for arguments is opaque to us —
// the MCP server defines it, we just pass it through.
type Tool struct {
	Type     string       `json:"type"` // always "function" today
	Function ToolFunction `json:"function"`
}

type ToolFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall is what the model returns when it wants to invoke a tool.
type ToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON-encoded args
}

// ChatRequest is the input to /v1/chat/completions.
type ChatRequest struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	Tools       []Tool    `json:"tools,omitempty"`
	Stream      bool      `json:"stream,omitempty"`
}

// Choice mirrors the OpenAI choices[].
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse is the simplified response shape. Usage stats and
// streaming chunks are omitted on purpose — we don't need them
// in the orchestrator yet.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Choices []Choice `json:"choices"`
}

// HTTPClient is the concrete Client backed by net/http. Constructed
// once at boot.
type HTTPClient struct {
	baseURL string
	http    *http.Client
	model   string
}

// defaultInferenceTimeout applies when the caller passes no WithTimeout.
// Deployments set INFERENCE_TIMEOUT; this only guards a bare NewHTTP.
const defaultInferenceTimeout = 180 * time.Second

// Option configures the HTTPClient. Keep it tiny — orthogonal knobs
// per option, no struct sprawl.
type Option func(*HTTPClient)

// WithTimeout sets the per-request timeout. The default below is a floor, not
// a target: on CPU the whole prompt is ingested before the first token, so the
// budget must cover ingest + generation or the request dies mid-ingest.
func WithTimeout(d time.Duration) Option {
	return func(c *HTTPClient) {
		c.http.Timeout = d
	}
}

// WithModel sets the model name passed in the request. Most OpenAI-compat
// servers ignore this and serve whatever's loaded, but we send it for
// observability and compatibility.
func WithModel(name string) Option {
	return func(c *HTTPClient) {
		c.model = name
	}
}

// NewHTTP constructs an HTTPClient pointing at baseURL (e.g.
// http://slm-inference.support-platform.svc.cluster.local:8000).
func NewHTTP(baseURL string, opts ...Option) *HTTPClient {
	c := &HTTPClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: defaultInferenceTimeout},
		model:   "qwen2.5-1.5b-instruct",
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Chat calls /v1/chat/completions and returns the parsed response.
// Returns the request error (non-2xx becomes an *HTTPError carrying
// status and body).
func (c *HTTPClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.model
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("inference request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode/100 != 2 {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(respBody)}
	}
	var out ChatResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("inference returned no choices")
	}
	return &out, nil
}

// HTTPError is a transport-level error from the inference server.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("inference HTTP %d: %s", e.Status, e.Body)
}

// Ensure interface satisfaction at compile time.
var _ Client = (*HTTPClient)(nil)
