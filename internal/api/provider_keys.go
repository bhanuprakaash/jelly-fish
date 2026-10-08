package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/providerkeys"
)

// Sealer encrypts a secret under the primary master key. The api role never
// opens one (auth-keys.md §5.8), so the interface has no Open.
type Sealer interface {
	Seal(plaintext, aad []byte) (ct []byte, keyID string, err error)
}

// ModelLister calls a Provider's free models-list endpoint with a key, which
// checks the key and returns the models it can use. It returns
// provider.ErrKeyRejected when the Provider refuses the key.
type ModelLister interface {
	ListModels(ctx context.Context, key string) ([]provider.Model, error)
}

// ProviderKeyStore is what /api/provider-keys needs from storage
// (internal/providerkeys.Store satisfies it).
type ProviderKeyStore interface {
	Upsert(ctx context.Context, userID uuid.UUID, k providerkeys.Sealed) (providerkeys.Info, error)
	List(ctx context.Context, userID uuid.UUID) ([]providerkeys.Info, error)
	Delete(ctx context.Context, userID uuid.UUID, provider string) error
}

var _ ProviderKeyStore = (*providerkeys.Store)(nil)

// ProviderKeyConfig wires Provider Key settings into the server.
type ProviderKeyConfig struct {
	Store  ProviderKeyStore
	Sealer Sealer
	// Models has a lister for each Provider a key can be saved for.
	Models map[string]ModelLister
}

type providerKeyHandlers struct {
	cfg    ProviderKeyConfig
	logger *slog.Logger
}

type providerKeyJSON struct {
	Provider  string    `json:"provider"`
	Last4     string    `json:"last4"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toProviderKeyJSON(i providerkeys.Info) providerKeyJSON {
	return providerKeyJSON{Provider: i.Provider, Last4: i.Last4, UpdatedAt: i.UpdatedAt}
}

// handlePutKey checks key against the Provider, then seals and stores it. A
// key the Provider rejects, or a check that fails, writes nothing.
func (h *providerKeyHandlers) handlePutKey(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("p")
	lister, ok := h.cfg.Models[p]
	if !ok {
		writeError(w, http.StatusNotFound, "unknown provider")
		return
	}
	var req struct {
		Key string `json:"key"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	key := strings.TrimSpace(req.Key)
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	u, _ := auth.UserFrom(r.Context())

	models, err := lister.ListModels(r.Context(), key)
	if errors.Is(err, provider.ErrKeyRejected) {
		writeError(w, http.StatusUnprocessableEntity, "Key rejected")
		return
	}
	if err != nil {
		h.logger.Error("check provider key", "provider", p, "user_id", u.ID, "error", err)
		writeError(w, http.StatusBadGateway, "could not reach provider")
		return
	}
	ct, keyID, err := h.cfg.Sealer.Seal([]byte(key), providerkeys.AAD(u.ID, p))
	if err != nil {
		h.logger.Error("seal provider key", "provider", p, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save key")
		return
	}
	info, err := h.cfg.Store.Upsert(r.Context(), u.ID, providerkeys.Sealed{
		Provider: p, Ciphertext: ct, KeyID: keyID, Last4: lastN(key, 4),
		Models: models,
	})
	if err != nil {
		h.logger.Error("save provider key", "provider", p, "user_id", u.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not save key")
		return
	}
	h.logger.Info("provider key saved", "provider", p, "user_id", u.ID)
	writeJSON(w, http.StatusOK, toProviderKeyJSON(info))
}

func (h *providerKeyHandlers) handleListKeys(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	infos, err := h.cfg.Store.List(r.Context(), u.ID)
	if err != nil {
		h.logger.Error("list provider keys", "user_id", u.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list keys")
		return
	}
	out := make([]providerKeyJSON, len(infos))
	for i, info := range infos {
		out[i] = toProviderKeyJSON(info)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *providerKeyHandlers) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	p := r.PathValue("p")
	err := h.cfg.Store.Delete(r.Context(), u.ID, p)
	if errors.Is(err, providerkeys.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no key saved")
		return
	}
	if err != nil {
		h.logger.Error("delete provider key", "provider", p, "user_id", u.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete key")
		return
	}
	h.logger.Info("provider key deleted", "provider", p, "user_id", u.ID)
	w.WriteHeader(http.StatusNoContent)
}

// lastN returns the last n characters of s.
func lastN(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[len(r)-n:])
}
