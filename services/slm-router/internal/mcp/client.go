// Package mcp talks to per-product MCP servers over the HTTP transport
// of the Model Context Protocol (JSON-RPC 2.0 inside HTTP POST bodies).
//
// Two operations matter to the orchestrator:
//
//	tools/list  — discover the tools a server exposes (used at startup
//	              and on tenant config reload)
//	tools/call  — invoke one tool with arguments (used when the model
//	              emits a tool_call)
//
// Auth is via a header carrying a secret value (the actual secret comes
// from an env var the deployment plumbs in from ExternalSecrets; the
// YAML tenant config carries only the env-var name).
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ServerRef describes one MCP server. Constructed from the YAML
// tenant config; passed into Call/ListTools per invocation.
type ServerRef struct {
	Name       string
	URL        string
	AuthHeader string
	AuthEnvVar string
}

// Tool is one entry returned by tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is the orchestrator-facing surface.
type Client interface {
	ListTools(ctx context.Context, server ServerRef) ([]Tool, error)
	Call(ctx context.Context, server ServerRef, toolName string, arguments map[string]any) (CallResult, error)
}

// CallResult is the tool-execution result. Content is the natural
// language to feed back into the model; IsError marks tool failures
// so the orchestrator can apply its escalation policy.
type CallResult struct {
	Content []ContentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// ContentBlock mirrors MCP's content array; for now we only handle text.
type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// HTTPClient is the concrete Client backed by net/http.
type HTTPClient struct {
	http *http.Client
	now  func() time.Time
}

// NewHTTP returns a client with sensible defaults. 15s per call —
// MCP servers wrap real product APIs which can occasionally be slow.
func NewHTTP() *HTTPClient {
	return &HTTPClient{
		http: &http.Client{Timeout: 15 * time.Second},
		now:  time.Now,
	}
}

// rpcRequest / rpcResponse implement JSON-RPC 2.0 message framing.
type rpcRequest struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      string         `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	return fmt.Sprintf("MCP RPC error %d: %s", e.Code, e.Message)
}

// ListTools calls "tools/list".
func (c *HTTPClient) ListTools(ctx context.Context, server ServerRef) ([]Tool, error) {
	resp, err := c.do(ctx, server, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var out struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal(resp, &out); err != nil {
		return nil, fmt.Errorf("decode tools/list: %w", err)
	}
	return out.Tools, nil
}

// Call invokes "tools/call".
func (c *HTTPClient) Call(ctx context.Context, server ServerRef, toolName string, arguments map[string]any) (CallResult, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}
	resp, err := c.do(ctx, server, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	var out CallResult
	if err := json.Unmarshal(resp, &out); err != nil {
		return CallResult{}, fmt.Errorf("decode tools/call: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) do(ctx context.Context, server ServerRef, method string, params map[string]any) (json.RawMessage, error) {
	id := fmt.Sprintf("%s-%d", method, c.now().UnixNano())
	rpc := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	body, err := json.Marshal(rpc)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if server.AuthHeader != "" && server.AuthEnvVar != "" {
		req.Header.Set(server.AuthHeader, os.Getenv(server.AuthEnvVar))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("MCP request to %s: %w", server.Name, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("MCP HTTP %d from %s: %s", resp.StatusCode, server.Name, string(respBody))
	}
	var rpcResp rpcResponse
	if err := json.Unmarshal(respBody, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error
	}
	return rpcResp.Result, nil
}

var _ Client = (*HTTPClient)(nil)
