// Package gemini talks to Google's Gemini API on a User's own key
// (docs/design/provider-gateway.md).
package gemini

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// Client calls Gemini. The zero value uses the public API.
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
	// Logger records why a reply was stopped; nil logs nothing.
	Logger *slog.Logger
}

// Catalog looks up a model's limits and capabilities.
type Catalog interface {
	Lookup(id string) (provider.ModelInfo, bool)
}

// ListModels lists every model key can use, without the "models/" prefix
// Gemini puts on ids. It is free, so it doubles as the key check. It does
// not retry: a rejected key must fail at once. Errors never carry key.
func (c Client) ListModels(ctx context.Context, key string) ([]provider.Model, error) {
	client, err := c.sdk(ctx, key)
	if err != nil {
		return nil, errors.New("list models: request failed")
	}

	var models []provider.Model
	for m, err := range client.Models.All(ctx) {
		if err != nil {
			var apiErr genai.APIError
			if errors.As(err, &apiErr) {
				if keyRejected(apiErr) {
					return nil, provider.ErrKeyRejected
				}
				return nil, fmt.Errorf("list models: status %d", apiErr.Code)
			}
			return nil, errors.New("list models: request failed")
		}
		id := strings.TrimPrefix(m.Name, "models/")
		models = append(models, provider.Model{ID: id, DisplayName: cmp.Or(m.DisplayName, id)})
	}
	return models, nil
}

// Provider returns a Provider that streams replies on key.
func (c Client) Provider(key string) provider.Provider {
	return chat{client: c, key: key, logger: cmp.Or(c.Logger, slog.New(slog.DiscardHandler))}
}

// sdk builds the SDK client on key. It retries nothing unless asked to:
// retries belong to our own error layer, which counts them (agent-loop.md
// §5.3).
func (c Client) sdk(ctx context.Context, key string) (*genai.Client, error) {
	return genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      key,
		Backend:     genai.BackendGeminiAPI,
		HTTPClient:  provider.WatchedClient(c.HTTPClient, c.IdleTimeout),
		HTTPOptions: genai.HTTPOptions{BaseURL: c.BaseURL},
	})
}
