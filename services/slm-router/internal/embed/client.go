// Package embed talks to the embedder service.
//
// The embedder is HuggingFace Text Embeddings Inference (TEI) running
// `ghcr.io/huggingface/text-embeddings-inference:cpu-1.8`. Its contract:
//
//	POST /embed
//	  request:  {"inputs": "text"} or {"inputs": ["t1", "t2"]}
//	  response: [[float, ...], [float, ...]]    ← bare array, no wrapper
//
// We always pass an array and unmarshal a bare array of vectors.
// Dimension is fixed per deployed model (bge-small-en is 384) and is
// checked against an expected value if WithExpectedDim is set.
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

// request matches the TEI shape exactly. The field name MUST be
// `inputs` — TEI returns "missing field `inputs`" 422 otherwise.
type request struct {
	Inputs []string `json:"inputs"`
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
	body, err := json.Marshal(request{Inputs: texts})
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
	// TEI returns a bare `[[float, ...], ...]` — no envelope. Unmarshal
	// straight into the result slice.
	var out [][]float32
	if err := json.Unmarshal(respBody, &out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(out) != len(texts) {
		return nil, fmt.Errorf("embed returned %d vectors for %d texts", len(out), len(texts))
	}
	if c.expectedDim > 0 {
		for i, v := range out {
			if len(v) != c.expectedDim {
				return nil, fmt.Errorf("embed vector %d has dim %d, expected %d", i, len(v), c.expectedDim)
			}
		}
	}
	return out, nil
}

var _ Client = (*HTTPClient)(nil)
var _ = errors.New // keep imports tidy across go.mod regenerations
