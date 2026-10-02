// Package anthropic talks to Anthropic's API on a User's own key
// (docs/design/provider-gateway.md).
package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Client calls Anthropic. The zero value uses the public API.
type Client struct {
	// BaseURL overrides the API origin, for tests.
	BaseURL string
	// HTTPClient overrides the transport, for tests.
	HTTPClient *http.Client
}

// ListModels lists every model key can use. It is free, so it doubles as the
// key check. It does not retry: a rejected key must fail at once. Errors
// never carry key.
func (c Client) ListModels(ctx context.Context, key string) ([]provider.Model, error) {
	opts := []option.RequestOption{option.WithAPIKey(key), option.WithMaxRetries(0)}
	if c.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(c.BaseURL))
	}
	if c.HTTPClient != nil {
		opts = append(opts, option.WithHTTPClient(c.HTTPClient))
	}
	client := sdk.NewClient(opts...)

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
