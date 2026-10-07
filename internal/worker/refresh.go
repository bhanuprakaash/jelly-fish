package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// modelRefreshInterval is how often each saved key's live models list is
// fetched again (provider-gateway.md §5.5).
const modelRefreshInterval = 24 * time.Hour

// ModelLister lists the models a key can use.
type ModelLister interface {
	ListModels(ctx context.Context, key string) ([]provider.Model, error)
}

// RefreshModels re-fetches the live models list of every saved Anthropic key
// and returns how many rows it updated. A row that fails is left as it was
// and does not stop the others; the failures come back joined.
func RefreshModels(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, lister ModelLister) (int, error) {
	users, err := keys.Owners(ctx, anthropic.Name)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, userID := range users {
		if err := refreshRow(ctx, keys, kr, lister, userID); err != nil {
			errs = append(errs, fmt.Errorf("refresh models (user %s): %w", userID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

func refreshRow(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, lister ModelLister, userID uuid.UUID) error {
	key, ct, err := openKey(ctx, keys, kr, userID, anthropic.Name)
	if err != nil {
		return err
	}
	models, err := lister.ListModels(ctx, string(key))
	clear(key)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	return keys.SetModels(ctx, userID, anthropic.Name, ct, models)
}

// RunModelRefresh calls RefreshModels at start and then daily until ctx ends.
func RunModelRefresh(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, lister ModelLister, logger *slog.Logger) {
	for {
		n, err := RefreshModels(ctx, keys, kr, lister)
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
