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
	tools, err := c.ListTools(context.Background(), ServerRef{Name: "homechef-mcp", URL: srv.URL})
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
	_, err := c.Call(context.Background(), ServerRef{Name: "x", URL: srv.URL}, "nope", nil)
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
	res, err := c.Call(context.Background(), ServerRef{Name: "x", URL: srv.URL}, "lookup_order",
		map[string]any{"order_id": "123"})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Text != "order #123 is delivered" {
		t.Fatalf("res: got %+v", res)
	}
}
