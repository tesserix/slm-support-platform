package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPClientEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := make([][]float32, len(req.Texts))
		for i := range req.Texts {
			out[i] = []float32{float32(i), 0.5, -0.25, 1.0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{Embeddings: out})
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, WithExpectedDim(4))
	vecs, err := c.Embed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 || len(vecs[0]) != 4 {
		t.Fatalf("shape: got %dx%d want 2x4", len(vecs), len(vecs[0]))
	}
}

func TestHTTPClientEmbedDimMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{Embeddings: [][]float32{{1, 2, 3}}})
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, WithExpectedDim(4))
	_, err := c.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected dim mismatch error")
	}
}
