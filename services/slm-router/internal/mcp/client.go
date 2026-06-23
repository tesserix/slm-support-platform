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

// Canonical trusted-context header names. The mcp-gateway reads these
// EXACT names to attribute a tool call to the originating conversation
// and customer (traceability + ticket creation). Values originate
// server-side in slm-router (Mongo change-stream event + conversation
// document) — never from the model or the customer — so the gateway may
// trust them PROVIDED the shared auth header (ServerRef.AuthHeader) is
// present and valid on the same request.
const (
	HeaderConversationID = "X-Otto-Conversation-Id"
	HeaderTenantID       = "X-Tenant-Id"
	HeaderStoreID        = "X-Store-Id"
	HeaderCustomerID     = "X-Customer-Id"
	HeaderCustomerEmail  = "X-Customer-Email"
	HeaderCustomerName   = "X-Customer-Name"
	HeaderCaseID         = "X-Otto-Case-Id"
)

// CallContext is the trusted conversation/customer identity forwarded on
// every MCP request as HTTP headers. Passed by value; zero value is
// valid (the startup pre-warm tools/list sends no context headers).
// Empty fields are omitted so the gateway can tell "unknown" from "".
type CallContext struct {
	ConversationID string
	TenantID       string
	StoreID        string
	CustomerID     string // internal product user id (firebase uid / Keycloak sub / UUID)
	CustomerEmail  string
	CustomerName   string
	CaseID         string // Otto support case id when one exists; else ""
}

// applyTrustedHeaders writes the canonical context headers onto req,
// omitting empty fields.
func applyTrustedHeaders(req *http.Request, cc CallContext) {
	setIf := func(name, val string) {
		if val != "" {
			req.Header.Set(name, val)
		}
	}
	setIf(HeaderConversationID, cc.ConversationID)
	setIf(HeaderTenantID, cc.TenantID)
	setIf(HeaderStoreID, cc.StoreID)
	setIf(HeaderCustomerID, cc.CustomerID)
	setIf(HeaderCustomerEmail, cc.CustomerEmail)
	setIf(HeaderCustomerName, cc.CustomerName)
	setIf(HeaderCaseID, cc.CaseID)
}

// Tool is one entry returned by tools/list.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// Client is the orchestrator-facing surface.
type Client interface {
	ListTools(ctx context.Context, server ServerRef, cc CallContext) ([]Tool, error)
	Call(ctx context.Context, server ServerRef, cc CallContext, toolName string, arguments map[string]any) (CallResult, error)
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
func (c *HTTPClient) ListTools(ctx context.Context, server ServerRef, cc CallContext) ([]Tool, error) {
	resp, err := c.do(ctx, server, cc, "tools/list", nil)
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
func (c *HTTPClient) Call(ctx context.Context, server ServerRef, cc CallContext, toolName string, arguments map[string]any) (CallResult, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": arguments,
	}
	resp, err := c.do(ctx, server, cc, "tools/call", params)
	if err != nil {
		return CallResult{}, err
	}
	var out CallResult
	if err := json.Unmarshal(resp, &out); err != nil {
		return CallResult{}, fmt.Errorf("decode tools/call: %w", err)
	}
	return out, nil
}

func (c *HTTPClient) do(ctx context.Context, server ServerRef, cc CallContext, method string, params map[string]any) (json.RawMessage, error) {
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
	// Trusted conversation/customer context for traceability + ticket
	// creation. Set AFTER the shared-secret auth header; never derived
	// from model/customer input.
	applyTrustedHeaders(req, cc)
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
