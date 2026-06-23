package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientListTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "tools/list" {
			t.Fatalf("method: got %s want tools/list", req.Method)
		}
		result := json.RawMessage(`{"tools":[{"name":"lookup_order","description":"...","inputSchema":{}}]}`)
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	}))
	defer srv.Close()

	c := NewHTTP()
	tools, err := c.ListTools(context.Background(), ServerRef{Name: "homechef-mcp", URL: srv.URL}, CallContext{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "lookup_order" {
		t.Fatalf("tools: got %+v", tools)
	}
}

func TestHTTPClientCallToolError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(rpcResponse{
			JSONRPC: "2.0", ID: req.ID,
			Error: &rpcError{Code: -32601, Message: "unknown tool"},
		})
	}))
	defer srv.Close()

	c := NewHTTP()
	_, err := c.Call(context.Background(), ServerRef{Name: "x", URL: srv.URL}, CallContext{}, "nope", nil)
	if err == nil {
		t.Fatal("expected rpc error")
	}
	rpcErr, ok := err.(*rpcError)
	if !ok {
		t.Fatalf("err type: got %T", err)
	}
	if rpcErr.Code != -32601 {
		t.Fatalf("code: got %d want -32601", rpcErr.Code)
	}
}

func TestHTTPClientCallToolSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		result := json.RawMessage(`{"content":[{"type":"text","text":"order #123 is delivered"}],"isError":false}`)
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: result})
	}))
	defer srv.Close()

	c := NewHTTP()
	res, err := c.Call(context.Background(), ServerRef{Name: "x", URL: srv.URL}, CallContext{}, "lookup_order",
		map[string]any{"order_id": "123"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "order #123 is delivered" {
		t.Fatalf("res: got %+v", res)
	}
}

// TestHTTPClientForwardsTrustedHeaders verifies the canonical context
// headers are sent on a tools/call (and that empty fields are omitted —
// CaseID is "" here so its header must be absent).
func TestHTTPClientForwardsTrustedHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		var req rpcRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: json.RawMessage(`{"content":[],"isError":false}`)})
	}))
	defer srv.Close()

	cc := CallContext{ConversationID: "conv-1", TenantID: "t-1", StoreID: "s-1", CustomerID: "u-1", CustomerEmail: "a@b.c", CustomerName: "Ann"}
	_, err := NewHTTP().Call(context.Background(), ServerRef{Name: "x", URL: srv.URL}, cc, "t", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	for h, want := range map[string]string{
		HeaderConversationID: "conv-1", HeaderTenantID: "t-1", HeaderStoreID: "s-1",
		HeaderCustomerID: "u-1", HeaderCustomerEmail: "a@b.c", HeaderCustomerName: "Ann",
	} {
		if got.Get(h) != want {
			t.Fatalf("header %s: got %q want %q", h, got.Get(h), want)
		}
	}
	if got.Get(HeaderCaseID) != "" {
		t.Fatalf("empty CaseID must be omitted, got %q", got.Get(HeaderCaseID))
	}
}
