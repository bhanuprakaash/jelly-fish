package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// RotateKeys re-seals every provider key not sealed under kr's primary key,
// one row per transaction, and returns how many it moved
// (auth-keys.md §5.9). A row that fails is left as it was and does not stop
// the others, so one bad row can't hold every other owner on a retiring key;
// the failures come back joined.
func RotateKeys(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring) (int, error) {
	stale, err := keys.Stale(ctx, kr.Primary())
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, k := range stale {
		aad := providerkeys.AAD(k.UserID, k.Provider)
		moved, err := keys.Reseal(ctx, k, kr.Primary(), func(ct []byte, keyID string) ([]byte, string, error) {
			plaintext, err := kr.Open(ct, keyID, aad)
			if err != nil {
				return nil, "", fmt.Errorf("open: %w", err)
			}
			defer clear(plaintext)
			ct, keyID, err = kr.Seal(plaintext, aad)
			if err != nil {
				return nil, "", fmt.Errorf("seal: %w", err)
			}
			return ct, keyID, nil
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("rotate provider key (user %s, provider %s): %w", k.UserID, k.Provider, err))
			continue
		}
		if moved {
			n++
		}
	}
	return n, errors.Join(errs...)
}
