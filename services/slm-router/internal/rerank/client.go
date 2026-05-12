// Package rerank talks to the reranker service.
//
// Contract: POST /rerank accepts {"query": "...", "documents": [...]}
// and returns {"scores": [...float64...]} aligned with the documents.
// Higher = more relevant. Caller does the top-K trimming.
package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"time"
)

// Client is the orchestrator-facing surface.
type Client interface {
	// Rerank returns scores aligned with documents (same length, same order).
	Rerank(ctx context.Context, query string, documents []string) ([]float64, error)
}

type request struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
}

type response struct {
	Scores []float64 `json:"scores"`
}

// HTTPClient is the concrete Client backed by net/http.
type HTTPClient struct {
	baseURL string
	http    *http.Client
}

// NewHTTP constructs a client. Default timeout 10s — reranking 50
// chunks via a CPU cross-encoder runs in well under a second.
func NewHTTP(baseURL string) *HTTPClient {
	return &HTTPClient{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
}

// Rerank scores each document against the query.
func (c *HTTPClient) Rerank(ctx context.Context, query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(request{Query: query, Documents: documents})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/rerank", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("rerank HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var out response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(out.Scores) != len(documents) {
		return nil, fmt.Errorf("rerank returned %d scores for %d documents", len(out.Scores), len(documents))
	}
	return out.Scores, nil
}

var _ Client = (*HTTPClient)(nil)

// TopK is a helper: given documents and parallel scores, return the
// indices of the top-K documents by score. Doesn't mutate input.
func TopK(scores []float64, k int) []int {
	if k <= 0 {
		return nil
	}
	type pair struct {
		idx   int
		score float64
	}
	pairs := make([]pair, len(scores))
	for i, s := range scores {
		pairs[i] = pair{i, s}
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].score > pairs[j].score })
	if k > len(pairs) {
		k = len(pairs)
	}
	out := make([]int, k)
	for i := 0; i < k; i++ {
		out[i] = pairs[i].idx
	}
	return out
}
