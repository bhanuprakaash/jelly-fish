package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// modelRefreshInterval is how often each saved key's live models list is
// fetched again (provider-gateway.md §5.5).
const modelRefreshInterval = 24 * time.Hour

// RefreshModels re-fetches the live models list of every saved key of each
// Provider in adapters and returns how many rows it updated. A row that
// fails is left as it was and does not stop the others; the failures come
// back joined.
func RefreshModels(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, adapters map[string]Adapter) (int, error) {
	n := 0
	var errs []error
	for _, prov := range slices.Sorted(maps.Keys(adapters)) {
		users, err := keys.Owners(ctx, prov)
		if err != nil {
			return n, errors.Join(append(errs, err)...)
		}
		for _, userID := range users {
			if err := refreshRow(ctx, keys, kr, adapters[prov], prov, userID); err != nil {
				errs = append(errs, fmt.Errorf("refresh %s models (user %s): %w", prov, userID, err))
				continue
			}
			n++
		}
	}
	return n, errors.Join(errs...)
}

func refreshRow(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, adapter Adapter, prov string, userID uuid.UUID) error {
	key, ct, err := openKey(ctx, keys, kr, userID, prov)
	if err != nil {
		return err
	}
	models, err := adapter.ListModels(ctx, string(key))
	clear(key)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	return keys.SetModels(ctx, userID, prov, ct, models)
}

// RunModelRefresh calls RefreshModels at start and then daily until ctx ends.
func RunModelRefresh(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, adapters map[string]Adapter, logger *slog.Logger) {
	for {
		n, err := RefreshModels(ctx, keys, kr, adapters)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.Warn("refresh models", "updated", n, "error", err)
		} else {
			logger.Info("models refreshed", "updated", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(modelRefreshInterval):
		}
	}
}
