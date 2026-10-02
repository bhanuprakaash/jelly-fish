package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
)

// loginChecker tells an open stream whether its Login Session still stands.
type loginChecker interface {
	LoginActive(ctx context.Context, id uuid.UUID) (bool, error)
}

func (a *authHandlers) handleLogout(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	err := a.cfg.Authenticator.DeleteLoginSession(r.Context(), u.ID, u.LoginSessionID)
	if err != nil && !errors.Is(err, auth.ErrNotFound) {
		a.logger.Error("logout", "error", err)
		writeError(w, http.StatusInternalServerError, "could not log out")
		return
	}
	a.clearLoginCookie(w)
}

func (a *authHandlers) handleLogoutAll(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	if err := a.cfg.Authenticator.DeleteLoginSessions(r.Context(), u.ID); err != nil {
		a.logger.Error("logout all", "error", err)
		writeError(w, http.StatusInternalServerError, "could not log out")
		return
	}
	a.logger.Info("logout all", "user_id", u.ID)
	a.clearLoginCookie(w)
}

func (a *authHandlers) clearLoginCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Path:     "/",
		MaxAge:   -1,
		Secure:   !a.cfg.InsecureCookie,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

type loginSessionJSON struct {
	ID         uuid.UUID `json:"id"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	Current    bool      `json:"current"`
}

func (a *authHandlers) handleListLoginSessions(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	list, err := a.cfg.Authenticator.LoginSessions(r.Context(), u.ID)
	if err != nil {
		a.logger.Error("list login sessions", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list devices")
		return
	}
	out := make([]loginSessionJSON, len(list))
	for i, ls := range list {
		out[i] = loginSessionJSON{
			ID: ls.ID, UserAgent: ls.UserAgent, CreatedAt: ls.CreatedAt, LastSeenAt: ls.LastSeenAt,
			Current: ls.ID == u.LoginSessionID,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *authHandlers) handleDeleteLoginSession(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	u, _ := auth.UserFrom(r.Context())
	err = a.cfg.Authenticator.DeleteLoginSession(r.Context(), u.ID, id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	if err != nil {
		a.logger.Error("delete login session", "error", err)
		writeError(w, http.StatusInternalServerError, "could not log out device")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
