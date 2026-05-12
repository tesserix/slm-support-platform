package inference

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientChat(t *testing.T) {
	want := ChatResponse{
		ID:     "id-1",
		Object: "chat.completion",
		Choices: []Choice{
			{
				Index:        0,
				Message:      Message{Role: RoleAssistant, Content: "hello"},
				FinishReason: "stop",
			},
		},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method: got %s want POST", r.Method)
		}
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path: got %s want /v1/chat/completions", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL)
	got, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "hi"},
		},
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if got.Choices[0].Message.Content != "hello" {
		t.Fatalf("content: got %q want hello", got.Choices[0].Message.Content)
	}
}

func TestHTTPClientChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "model busy", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL)
	_, err := c.Chat(context.Background(), ChatRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error on 503")
	}
	httpErr, ok := err.(*HTTPError)
	if !ok {
		t.Fatalf("err type: got %T want *HTTPError", err)
	}
	if httpErr.Status != 503 {
		t.Fatalf("status: got %d want 503", httpErr.Status)
	}
}
