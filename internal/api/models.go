package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/eventlog"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/catalog"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// ModelLists returns the stored live models list of each of a User's saved
// keys, by provider (internal/providerkeys.Store satisfies it).
type ModelLists interface {
	Models(ctx context.Context, userID uuid.UUID) (map[string][]provider.Model, error)
}

var _ ModelLists = (*providerkeys.Store)(nil)

var _ ModelCatalog = catalog.Catalog{}

// ModelCatalog is the Model Catalog (internal/provider/catalog.Catalog).
type ModelCatalog interface {
	All() []provider.ModelInfo
	Lookup(id string) (provider.ModelInfo, bool)
}

// ModelConfig wires GET /api/models and model choice into the server.
type ModelConfig struct {
	Lists   ModelLists
	Catalog ModelCatalog
	// Default is the model new sessions use.
	Default string
	// Fake is the Fake Provider's model in dev builds; empty otherwise.
	Fake string
}

type modelJSON struct {
	ID            string           `json:"id"`
	DisplayName   string           `json:"display_name,omitempty"`
	ContextWindow int64            `json:"context_window,omitempty"`
	MaxOutput     int64            `json:"max_output,omitempty"`
	Price         *provider.Prices `json:"price"`
}

type providerModelsJSON struct {
	Provider string `json:"provider"`
	// Available is false for a Provider with no saved key: its models are
	// shown but can't be picked.
	Available bool        `json:"available"`
	Models    []modelJSON `json:"models"`
}

// pickable lists userID's models per Provider from the DB and the catalog
// only; it never calls a Provider (provider-gateway.md §4.2). A Provider
// with a key lists what its live list says, with limits from it where it
// has them and prices from the catalog. One without lists its catalog
// models, unavailable.
func (c ModelConfig) pickable(ctx context.Context, userID uuid.UUID) ([]providerModelsJSON, error) {
	lists, err := c.Lists.Models(ctx, userID)
	if err != nil {
		return nil, err
	}
	var out []providerModelsJSON
	for _, p := range []string{"anthropic", "openai", "gemini"} {
		live, ok := lists[p]
		group := providerModelsJSON{Provider: p, Available: ok, Models: []modelJSON{}}
		if ok {
			for _, m := range live {
				group.Models = append(group.Models, c.merge(m))
			}
		} else {
			for _, info := range c.Catalog.All() {
				if info.Provider == p {
					group.Models = append(group.Models, modelJSON{ID: info.ID, ContextWindow: info.ContextWindow, MaxOutput: info.MaxOutput, Price: info.Price})
				}
			}
		}
		out = append(out, group)
	}
	if c.Fake != "" {
		out = append(out, providerModelsJSON{Provider: c.Fake, Available: true, Models: []modelJSON{{ID: c.Fake, DisplayName: "Fake (echo)"}}})
	}
	return out, nil
}

func (c ModelConfig) merge(m provider.Model) modelJSON {
	out := modelJSON{ID: m.ID, DisplayName: m.DisplayName, ContextWindow: m.MaxInputTokens, MaxOutput: m.MaxTokens}
	info, ok := c.Catalog.Lookup(m.ID)
	if !ok {
		return out
	}
	if out.ContextWindow == 0 {
		out.ContextWindow = info.ContextWindow
	}
	if out.MaxOutput == 0 {
		out.MaxOutput = info.MaxOutput
	}
	out.Price = info.Price
	return out
}

type modelHandlers struct {
	cfg    ModelConfig
	logger *slog.Logger
}

func (h *modelHandlers) handleList(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	groups, err := h.cfg.pickable(r.Context(), u.ID)
	if err != nil {
		h.logger.Error("list models", "user_id", u.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list models")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"default": h.cfg.Default, "providers": groups})
}

// providerOf returns the Provider of the group listing model, and whether
// that group is available.
func providerOf(groups []providerModelsJSON, model string) (prov string, available bool) {
	for _, g := range groups {
		for _, m := range g.Models {
			if m.ID == model {
				return g.Provider, g.Available
			}
		}
	}
	return "", false
}

// handleChangeModel switches a session's model for its next turn. The model
// must be available to the User, and on the session's current Provider,
// since history isn't replayed across Providers (#5). The Fake Provider
// has no history of its own to replay, so switching to or from it is fine.
func (h *modelHandlers) handleChangeModel(repo SessionRepo) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "session not found")
		if !ok {
			return
		}
		var req struct {
			Model string `json:"model"`
		}
		if !decodeJSON(w, r, &req) {
			return
		}
		if req.Model == "" {
			writeError(w, http.StatusBadRequest, "model is required")
			return
		}
		scope := scopeFrom(r)
		current, err := repo.SessionModel(r.Context(), scope, id)
		if errors.Is(err, eventlog.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if err != nil {
			h.logger.Error("look up session model", "error", err, "session_id", id)
			writeError(w, http.StatusInternalServerError, "could not change model")
			return
		}
		groups, err := h.cfg.pickable(r.Context(), scope.UserID)
		if err != nil {
			h.logger.Error("list models", "error", err, "session_id", id)
			writeError(w, http.StatusInternalServerError, "could not change model")
			return
		}
		to, ok := providerOf(groups, req.Model)
		if !ok {
			writeError(w, http.StatusUnprocessableEntity, "model is not available")
			return
		}
		from, _ := providerOf(groups, current)
		if info, ok := h.cfg.Catalog.Lookup(current); from == "" && ok {
			from = info.Provider
		}
		// A model neither lists has no known Provider to keep to.
		if from != "" && from != to && from != h.cfg.Fake && to != h.cfg.Fake {
			writeError(w, http.StatusUnprocessableEntity, "switching to another provider is not supported yet")
			return
		}
		err = repo.ChangeModel(r.Context(), scope, id, req.Model)
		if errors.Is(err, eventlog.ErrNotFound) {
			writeError(w, http.StatusNotFound, "session not found")
			return
		}
		if err != nil {
			h.logger.Error("change model", "error", err, "session_id", id)
			writeError(w, http.StatusInternalServerError, "could not change model")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}
