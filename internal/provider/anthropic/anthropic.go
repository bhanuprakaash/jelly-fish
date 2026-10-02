// Package anthropic talks to Anthropic's API on a User's own key
// (docs/design/provider-gateway.md).
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Client calls Anthropic. The zero value uses the public API.
type Client struct {
	// BaseURL overrides the API origin, for tests.
	BaseURL string
	// HTTPClient overrides the transport, for tests.
	HTTPClient *http.Client
	// IdleTimeout cuts a stream that sends no bytes for this long; zero
	// means provider.IdleTimeout.
	IdleTimeout time.Duration
	// Catalog gives each model's output cap and thinking mode.
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
	pager := client.Models.ListAutoPaging(ctx, sdk.ModelListParams{})
	for pager.Next() {
		m := pager.Current()
		models = append(models, provider.Model{ID: m.ID, DisplayName: m.DisplayName, MaxInputTokens: m.MaxInputTokens, MaxTokens: m.MaxTokens})
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
