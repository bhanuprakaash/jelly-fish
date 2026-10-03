package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"

	"github.com/bhanuprakaash/jelly-fish/internal/keyring"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/anthropic"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/retry"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/tracing"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// modelRefreshInterval is how often each saved key's live models list is
// fetched again (provider-gateway.md §5.5).
const modelRefreshInterval = 24 * time.Hour

// Catalog looks up a model's Provider and limits.
type Catalog interface {
	Lookup(id string) (provider.ModelInfo, bool)
}

// Gateway builds the Provider each turn runs on, from the session's model
// and its owner's Provider Key.
type Gateway struct {
	Keyring   *keyring.Keyring
	Keys      *providerkeys.Store
	Catalog   Catalog
	Anthropic anthropic.Client
	// Fake serves the model named Fake.Name(); nil outside dev builds.
	Fake provider.Provider
	// Tracer records one span per call attempt; nil records none.
	Tracer trace.TracerProvider
	// Sleep waits between quick retries; nil sleeps on a real timer.
	Sleep func(ctx context.Context, d time.Duration) error
}

// errNoKey means the session's owner has saved no key for its Provider.
var errNoKey = errors.New("no provider key saved")

// forTurn returns the Provider for one turn of userID's session on model,
// retrying quick failures; onRetry runs before each retry. Each attempt gets
// a span tagged with ids. The key is opened here, per turn, and lives only in
// the returned value. A missing key is a key_invalid provider.Error.
func (g Gateway) forTurn(ctx context.Context, userID uuid.UUID, model string, ids tracing.IDs, onRetry func()) (provider.Provider, error) {
	p, err := g.forSideCall(ctx, userID, model, ids)
	if err != nil {
		return nil, err
	}
	sleep := g.Sleep
	if sleep == nil {
		sleep = retry.Sleep
	}
	return retry.Wrap(p, sleep, onRetry), nil
}

// forSideCall is forTurn without the retries, for calls that must fail fast
// and are never repeated (the Title).
func (g Gateway) forSideCall(ctx context.Context, userID uuid.UUID, model string, ids tracing.IDs) (provider.Provider, error) {
	p, err := g.pick(ctx, userID, model)
	if errors.Is(err, errNoKey) {
		return nil, &provider.Error{Kind: provider.KindKeyInvalid, Err: err}
	}
	if err != nil {
		return nil, err
	}
	if g.Tracer != nil {
		p = tracing.Wrap(p, g.Tracer, ids)
	}
	return p, nil
}

func (g Gateway) pick(ctx context.Context, userID uuid.UUID, model string) (provider.Provider, error) {
	if g.Fake != nil && model == g.Fake.Name() {
		return g.Fake, nil
	}
	prov, err := g.providerOf(ctx, userID, model)
	if err != nil {
		return nil, err
	}
	if prov != anthropic.Name {
		return nil, fmt.Errorf("provider %q is not supported", prov)
	}
	key, _, err := openKey(ctx, g.Keys, g.Keyring, userID, prov)
	if errors.Is(err, providerkeys.ErrNotFound) {
		return nil, fmt.Errorf("%s: %w", prov, errNoKey)
	}
	if err != nil {
		return nil, err
	}
	defer clear(key)
	return g.Anthropic.Provider(string(key)), nil
}

// providerOf names model's Provider: the catalog's, else the one whose
// saved key lists it live. Such a model runs with default limits and no
// price (provider-gateway.md D15). A model neither knows is
// model_unavailable.
func (g Gateway) providerOf(ctx context.Context, userID uuid.UUID, model string) (string, error) {
	if info, ok := g.Catalog.Lookup(model); ok {
		return info.Provider, nil
	}
	lists, err := g.Keys.Models(ctx, userID)
	if err != nil {
		return "", err
	}
	for prov, models := range lists {
		if slices.ContainsFunc(models, func(m provider.Model) bool { return m.ID == model }) {
			return prov, nil
		}
	}
	return "", &provider.Error{Kind: provider.KindModelUnavailable, Err: fmt.Errorf("model %q is unknown", model)}
}

// price is model's list price, or nil when the catalog has none for it.
func (g Gateway) price(model string) *provider.Prices {
	if g.Catalog == nil {
		return nil
	}
	info, _ := g.Catalog.Lookup(model)
	return info.Price
}

// openKey returns userID's plaintext key for prov, which the caller clears,
// and the ciphertext it came from.
func openKey(ctx context.Context, keys *providerkeys.Store, kr *keyring.Keyring, userID uuid.UUID, prov string) (key, ct []byte, err error) {
	sealed, err := keys.Sealed(ctx, userID, prov)
	if err != nil {
		return nil, nil, err
	}
	key, err = kr.Open(sealed.Ciphertext, sealed.KeyID, providerkeys.AAD(userID, prov))
	if err != nil {
		return nil, nil, fmt.Errorf("open %s key: %w", prov, err)
	}
	return key, sealed.Ciphertext, nil
}

// ModelLister lists the models a key can use.
type ModelLister interface {
	ListModels(ctx context.Context, key string) ([]provider.Model, error)
}

// RefreshModels re-fetches the live models list of every saved Anthropic key
// and returns how many rows it updated. A row that fails is left as it was
// and does not stop the others; the failures come back joined.
func RefreshModels(ctx context.Context, pool *pgxpool.Pool, kr *keyring.Keyring, lister ModelLister) (int, error) {
	rows, err := pool.Query(ctx, `SELECT user_id FROM provider_keys WHERE provider = $1`, anthropic.Name)
	if err != nil {
		return 0, fmt.Errorf("list provider keys: %w", err)
	}
	users, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, fmt.Errorf("list provider keys: %w", err)
	}

	keys := providerkeys.NewStore(pool)
	n := 0
	var errs []error
	for _, userID := range users {
		if err := refreshRow(ctx, pool, keys, kr, lister, userID); err != nil {
			errs = append(errs, fmt.Errorf("refresh models (user %s): %w", userID, err))
			continue
		}
		n++
	}
	return n, errors.Join(errs...)
}

func refreshRow(ctx context.Context, pool *pgxpool.Pool, keys *providerkeys.Store, kr *keyring.Keyring, lister ModelLister, userID uuid.UUID) error {
	key, ct, err := openKey(ctx, keys, kr, userID, anthropic.Name)
	if err != nil {
		return err
	}
	models, err := lister.ListModels(ctx, string(key))
	clear(key)
	if err != nil {
		return fmt.Errorf("list models: %w", err)
	}
	b, err := json.Marshal(models)
	if err != nil {
		return fmt.Errorf("marshal models: %w", err)
	}
	// Only the list changes: a key replaced meanwhile keeps its own list.
	if _, err := pool.Exec(ctx, `UPDATE provider_keys SET models = $3, models_fetched_at = now()
		WHERE user_id = $1 AND provider = $2 AND ciphertext = $4`, userID, anthropic.Name, b, ct); err != nil {
		return fmt.Errorf("store models: %w", err)
	}
	return nil
}

// RunModelRefresh calls RefreshModels at start and then daily until ctx ends.
func RunModelRefresh(ctx context.Context, pool *pgxpool.Pool, kr *keyring.Keyring, lister ModelLister, logger *slog.Logger) {
	for {
		n, err := RefreshModels(ctx, pool, kr, lister)
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
