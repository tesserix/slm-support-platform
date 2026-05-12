package retriever

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Postgres is the pgvector-backed Retriever. Constructed once at boot
// with an open *sql.DB; safe for concurrent use.
type Postgres struct {
	db *sql.DB
}

// NewPostgres wraps an open *sql.DB. The caller owns the DB's lifecycle
// (connection pool, ping checks, shutdown).
func NewPostgres(db *sql.DB) *Postgres {
	return &Postgres{db: db}
}

// SearchNearest queries the chunks table for the K nearest neighbours
// of queryVec within namespace.
//
// We use a string-form vector literal (pgvector accepts "[1,2,3]") so
// this works with any standard database/sql driver — no pgvector-specific
// driver extensions needed. Drivers that DO speak pgvector natively
// (jackc/pgx with the pgvector adapter) would let us send []float32
// directly; switching is a one-line change inside this method.
func (p *Postgres) SearchNearest(ctx context.Context, namespace string, queryVec []float32, k int) ([]Chunk, error) {
	if namespace == "" {
		return nil, errors.New("namespace required")
	}
	if k <= 0 {
		return nil, errors.New("k must be positive")
	}
	if len(queryVec) == 0 {
		return nil, errors.New("queryVec required")
	}

	const q = `
		SELECT id, content, metadata, (embedding <=> $1::vector) AS distance
		FROM chunks
		WHERE namespace = $2
		ORDER BY embedding <=> $1::vector
		LIMIT $3
	`

	rows, err := p.db.QueryContext(ctx, q, vectorLiteral(queryVec), namespace, k)
	if err != nil {
		return nil, fmt.Errorf("pgvector query: %w", err)
	}
	defer rows.Close()

	var out []Chunk
	for rows.Next() {
		var (
			id       string
			content  string
			metaRaw  sql.NullString
			distance float64
		)
		if err := rows.Scan(&id, &content, &metaRaw, &distance); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		var meta map[string]any
		if metaRaw.Valid && metaRaw.String != "" {
			if err := json.Unmarshal([]byte(metaRaw.String), &meta); err != nil {
				// Bad metadata blob shouldn't kill retrieval; log via
				// the caller's slog after returning.
				meta = map[string]any{"_metadata_parse_error": err.Error()}
			}
		}
		out = append(out, Chunk{
			ID:       id,
			Content:  content,
			Metadata: meta,
			Distance: distance,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

var _ Retriever = (*Postgres)(nil)
