// Package retriever pulls top-K chunks from the support-postgres
// pgvector store, scoped by per-product namespace.
//
// Schema (mirrored in tesserix-k8s db-schema-bootstrap):
//
//   CREATE TABLE chunks (
//     id          TEXT PRIMARY KEY,
//     namespace   TEXT NOT NULL,
//     content     TEXT NOT NULL,
//     embedding   VECTOR(384) NOT NULL,
//     metadata    JSONB,
//     created_at  TIMESTAMPTZ DEFAULT NOW()
//   );
//   CREATE INDEX idx_chunks_namespace ON chunks (namespace);
//   CREATE INDEX idx_chunks_embedding ON chunks USING hnsw (embedding vector_cosine_ops);
//
// The HNSW index gives sub-millisecond approximate nearest-neighbour
// queries. We use cosine distance because bge-small-en embeddings are
// L2-normalised at production time.
package retriever

import (
	"context"
	"fmt"
	"strings"
)

// Chunk is one retrieved document chunk plus its distance to the query.
type Chunk struct {
	ID       string
	Content  string
	Metadata map[string]any
	// Distance is the pgvector cosine distance (0 = identical, 2 = opposite).
	// Lower is more relevant.
	Distance float64
}

// Retriever is the orchestrator-facing surface. Mockable for tests.
type Retriever interface {
	// SearchNearest returns up to k chunks in the namespace, ordered
	// nearest-first by cosine distance to queryVec.
	SearchNearest(ctx context.Context, namespace string, queryVec []float32, k int) ([]Chunk, error)
}

// vectorLiteral renders a Go []float32 in pgvector's textual literal
// format: "[0.1,0.2,...]". Used by SQL drivers that don't natively
// understand the vector type.
func vectorLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, x := range v {
		parts[i] = formatFloat(x)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// formatFloat keeps the literal compact but lossless. We use 'g'
// formatting with enough precision to represent a float32 exactly.
func formatFloat(f float32) string {
	return fmt.Sprintf("%g", f)
}
