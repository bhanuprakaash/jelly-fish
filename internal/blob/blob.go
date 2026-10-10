// Package blob stores payloads too large for an event inline, keyed by their
// sha256 (docs/design/event-log.md §3).
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	// InlineLimit is the largest payload part an event keeps inline.
	InlineLimit = 32 << 10
	// PreviewLen is how many bytes of a blob's text an event keeps beside
	// its Ref.
	PreviewLen = 2 << 10
)

// Ref points at a blob from an event payload. Key is where the bytes are
// fetched from; it equals SHA256 until an object store gives it a path.
type Ref struct {
	Key    string `json:"key"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
	Mime   string `json:"mime"`
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Put stores data in tx, so it lands with the event that references it. The
// same content stored twice is one row.
func Put(ctx context.Context, tx pgx.Tx, mime string, data []byte) (Ref, error) {
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	_, err := tx.Exec(ctx, `
		INSERT INTO blobs (sha256, mime, size, data) VALUES ($1, $2, $3, $4)
		ON CONFLICT (sha256) DO NOTHING`, sha, mime, len(data), data)
	if err != nil {
		return Ref{}, fmt.Errorf("insert blob: %w", err)
	}
	return Ref{Key: sha, Size: len(data), SHA256: sha, Mime: mime}, nil
}

// Get returns the blob with the given sha256, or pgx.ErrNoRows.
func Get(ctx context.Context, q querier, sha string) (mime string, data []byte, err error) {
	if err := q.QueryRow(ctx, `SELECT mime, data FROM blobs WHERE sha256 = $1`, sha).Scan(&mime, &data); err != nil {
		return "", nil, fmt.Errorf("get blob: %w", err)
	}
	return mime, data, nil
}
