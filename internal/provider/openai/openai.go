// Package openai talks to OpenAI's Responses API on a User's own key
// (docs/design/provider-gateway.md).
package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Client calls OpenAI. The zero value uses the public API.
type Client struct {
	// BaseURL overrides the API origin, for tests.
	BaseURL string
	// HTTPClient overrides the transport, for tests.
	HTTPClient *http.Client
	// IdleTimeout cuts a stream that sends no bytes for this long; zero
	// means provider.IdleTimeout.
	IdleTimeout time.Duration
	// Catalog gives each model's capabilities.
	Catalog Catalog
}

// Catalog looks up a model's limits and capabilities.
type Catalog interface {
	Lookup(id string) (provider.ModelInfo, bool)
}

// ListModels lists every model key can use. It is free, so it doubles as the
// key check. It does not retry: a rejected key must fail at once. Errors
// never carry key.
func (c Client) ListModels(ctx context.Context, key string) ([]provider.Model, error) {
	client := sdk.NewClient(c.options(key)...)

	var models []provider.Model
	pager := client.Models.ListAutoPaging(ctx)
	for pager.Next() {
		m := pager.Current()
		models = append(models, provider.Model{ID: m.ID, DisplayName: m.ID})
	}
	if err := pager.Err(); err != nil {
		var apiErr *sdk.Error
		if errors.As(err, &apiErr) {
			if apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden {
				return nil, provider.ErrKeyRejected
			}
			return nil, fmt.Errorf("list models: status %d", apiErr.StatusCode)
		}
		return nil, errors.New("list models: request failed")
	}
	return models, nil
}

// Provider returns a Provider that streams replies on key.
func (c Client) Provider(key string) provider.Provider {
	return chat{client: sdk.NewClient(c.options(key)...), catalog: c.Catalog}
}

func (c Client) options(key string) []option.RequestOption {
	// Retries belong to our own error layer, which counts them
	// (agent-loop.md §5.3), never to the SDK.
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	return append(opts, option.WithHTTPClient(provider.WatchedClient(c.HTTPClient, c.IdleTimeout)))
}
