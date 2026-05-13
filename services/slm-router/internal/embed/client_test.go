package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TEI returns a bare `[[float, ...], ...]` envelope. These tests stand in for
// the real embedder so we lock the wire shape down at build time.

func TestHTTPClientEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		out := make([][]float32, len(req.Inputs))
		for i := range req.Inputs {
			out[i] = []float32{float32(i), 0.5, -0.25, 1.0}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
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
		_ = json.NewEncoder(w).Encode([][]float32{{1, 2, 3}})
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL, WithExpectedDim(4))
	_, err := c.Embed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected dim mismatch error")
	}
}

// Regression: TEI requires the request field to be `inputs` exactly.
// Anything else gets a 422 "missing field `inputs`" which we surface
// as a useless error. Lock this name down so a rename in client.go
// can't silently revert the fix.
func TestHTTPClientEmbedSendsInputsField(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&raw)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([][]float32{{1, 2, 3, 4}})
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL)
	if _, err := c.Embed(context.Background(), []string{"a"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if _, ok := raw["inputs"]; !ok {
		t.Fatalf("expected `inputs` field on request body, got keys: %v", keysOf(raw))
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
