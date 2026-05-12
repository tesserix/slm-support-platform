package rerank

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestHTTPClientRerank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req request
		_ = json.NewDecoder(r.Body).Decode(&req)
		// Score = length of document (toy heuristic, deterministic).
		scores := make([]float64, len(req.Documents))
		for i, d := range req.Documents {
			scores[i] = float64(len(d))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(response{Scores: scores})
	}))
	defer srv.Close()

	c := NewHTTP(srv.URL)
	scores, err := c.Rerank(context.Background(), "q", []string{"a", "bb", "ccc"})
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if !reflect.DeepEqual(scores, []float64{1, 2, 3}) {
		t.Fatalf("scores: got %v want [1 2 3]", scores)
	}
}

func TestTopK(t *testing.T) {
	scores := []float64{0.1, 0.9, 0.5, 0.7}
	got := TopK(scores, 2)
	want := []int{1, 3} // 0.9, 0.7
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TopK: got %v want %v", got, want)
	}
}
