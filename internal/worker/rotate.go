package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

type staleKey struct {
	userID   uuid.UUID
	provider string
}

// RotateKeys re-seals every provider_keys row not sealed under kr's primary
// key, one row per transaction, and returns how many it moved
// (auth-keys.md §5.9). A row that fails is left as it was and does not stop
// the others, so one bad row can't hold every other owner on a retiring key;
// the failures come back joined.
func RotateKeys(ctx context.Context, pool *pgxpool.Pool, kr *keyring.Keyring) (int, error) {
	rows, err := pool.Query(ctx, `SELECT user_id, provider FROM provider_keys WHERE key_id <> $1`, kr.Primary())
	if err != nil {
		return 0, fmt.Errorf("list stale provider keys: %w", err)
	}
	stale, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (staleKey, error) {
		var k staleKey
		err := row.Scan(&k.userID, &k.provider)
		return k, err
	})
	if err != nil {
		return 0, fmt.Errorf("list stale provider keys: %w", err)
	}

	n := 0
	var errs []error
	for _, k := range stale {
		moved, err := rotateRow(ctx, pool, kr, k)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if moved {
			n++
		}
	}
	return n, errors.Join(errs...)
}

func rotateRow(ctx context.Context, pool *pgxpool.Pool, kr *keyring.Keyring, k staleKey) (moved bool, err error) {
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		var ct []byte
		var keyID string
		err := tx.QueryRow(ctx, `SELECT ciphertext, key_id FROM provider_keys WHERE user_id = $1 AND provider = $2 FOR UPDATE`,
			k.userID, k.provider).Scan(&ct, &keyID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("lock row: %w", err)
		}
		if keyID == kr.Primary() {
			return nil
		}
		aad := providerkeys.AAD(k.userID, k.provider)
		plaintext, err := kr.Open(ct, keyID, aad)
		if err != nil {
			return fmt.Errorf("open: %w", err)
		}
		sealed, newID, err := kr.Seal(plaintext, aad)
		clear(plaintext)
		if err != nil {
			return fmt.Errorf("seal: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE provider_keys SET ciphertext = $3, key_id = $4 WHERE user_id = $1 AND provider = $2`,
			k.userID, k.provider, sealed, newID); err != nil {
			return fmt.Errorf("update: %w", err)
		}
		moved = true
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("rotate provider key (user %s, provider %s): %w", k.userID, k.provider, err)
	}
	return moved, nil
}
