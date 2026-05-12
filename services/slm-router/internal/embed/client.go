// Package embed talks to the embedder service.
//
// Contract: POST /embed accepts {"texts": [...]} and returns
// {"embeddings": [[...float32...]]} in the same order. Dimension is
// fixed per deployed model (bge-small-en is 384) and is checked
// against an expected value if WithExpectedDim is set.
package embed

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
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type request struct {
	Texts []string `json:"texts"`
}

type response struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// HTTPClient is the concrete Client backed by net/http.
type HTTPClient struct {
	baseURL     string
	http        *http.Client
	expectedDim int // 0 = no check
}

// Option configures the HTTPClient.
type Option func(*HTTPClient)

// WithTimeout sets the per-request timeout. Embedding a batch of
// a few dozen chunks finishes in well under a second; default 10s.
func WithTimeout(d time.Duration) Option {
	return func(c *HTTPClient) { c.http.Timeout = d }
}

// WithExpectedDim fails Embed() responses that don't match this
// dimensionality. Catches "wrong model loaded" deployment mistakes
// before they corrupt the index.
func WithExpectedDim(dim int) Option {
	return func(c *HTTPClient) { c.expectedDim = dim }
}

// NewHTTP constructs an HTTPClient pointing at baseURL.
func NewHTTP(baseURL string, opts ...Option) *HTTPClient {
	c := &HTTPClient{baseURL: baseURL, http: &http.Client{Timeout: 10 * time.Second}}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Embed sends a batch of texts and returns one vector per text, in order.
func (c *HTTPClient) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(request{Texts: texts})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("embed HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	var out response
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(out.Embeddings) != len(texts) {
		return nil, fmt.Errorf("embed returned %d vectors for %d texts", len(out.Embeddings), len(texts))
	}
	if c.expectedDim > 0 {
		for i, v := range out.Embeddings {
			if len(v) != c.expectedDim {
				return nil, fmt.Errorf("embed vector %d has dim %d, expected %d", i, len(v), c.expectedDim)
			}
		}
	}
	return out.Embeddings, nil
}

var _ Client = (*HTTPClient)(nil)
var _ = errors.New // keep imports tidy across go.mod regenerations
