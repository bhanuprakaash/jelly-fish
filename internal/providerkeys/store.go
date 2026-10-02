// Package providerkeys stores each User's sealed Provider Keys
// (docs/design/auth-keys.md §3). It never sees plaintext.
package providerkeys

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when the User has no key for the Provider.
var ErrNotFound = errors.New("not found")

// Sealed is a key as stored: ciphertext, the master key that sealed it, and
// the provider's live models list from the save-time call.
type Sealed struct {
	Provider   string
	Ciphertext []byte
	KeyID      string
	Last4      string
	Models     []byte
}

// Info is what a User may see of a saved key.
type Info struct {
	Provider  string
	Last4     string
	UpdatedAt time.Time
}

// Store keeps Provider Keys in Postgres.
type Store struct {
	pool *pgxpool.Pool
}

// NewStore returns a Store over pool.
func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// AAD binds a ciphertext to its owner, so a sealed key copied to another User
// or Provider fails to open (auth-keys.md §3).
func AAD(userID uuid.UUID, provider string) []byte {
	return []byte(userID.String() + "|" + provider)
}

// Upsert saves or replaces userID's key for s.Provider.
func (s *Store) Upsert(ctx context.Context, userID uuid.UUID, k Sealed) (Info, error) {
	var info Info
	err := s.pool.QueryRow(ctx, `
		INSERT INTO provider_keys (user_id, provider, ciphertext, key_id, last4, models, models_fetched_at)
		VALUES ($1, $2, $3, $4, $5, $6, now())
		ON CONFLICT (user_id, provider) DO UPDATE SET
			ciphertext = EXCLUDED.ciphertext, key_id = EXCLUDED.key_id, last4 = EXCLUDED.last4,
			models = EXCLUDED.models, models_fetched_at = now(), updated_at = now()
		RETURNING provider, last4, updated_at`,
		userID, k.Provider, k.Ciphertext, k.KeyID, k.Last4, k.Models,
	).Scan(&info.Provider, &info.Last4, &info.UpdatedAt)
	if err != nil {
		return Info{}, fmt.Errorf("upsert provider key: %w", err)
	}
	return info, nil
}

// List returns userID's saved keys, by provider.
func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]Info, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT provider, last4, updated_at FROM provider_keys WHERE user_id = $1 ORDER BY provider`, userID)
	if err != nil {
		return nil, fmt.Errorf("list provider keys: %w", err)
	}
	defer rows.Close()
	infos := []Info{}
	for rows.Next() {
		var i Info
		if err := rows.Scan(&i.Provider, &i.Last4, &i.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan provider key: %w", err)
		}
		infos = append(infos, i)
	}
	return infos, rows.Err()
}

// Delete removes userID's key for provider, or returns ErrNotFound.
func (s *Store) Delete(ctx context.Context, userID uuid.UUID, provider string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM provider_keys WHERE user_id = $1 AND provider = $2`, userID, provider)
	if err != nil {
		return fmt.Errorf("delete provider key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
