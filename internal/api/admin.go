package api

import (
	"context"
	"errors"
	"net/http"
	netmail "net/mail"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
)

// UserAdmin is what the /api/admin routes need from the store
// (internal/auth.Store satisfies it).
type UserAdmin interface {
	CreateInvite(ctx context.Context, invitedBy uuid.UUID, email string) (inv auth.Invite, created bool, err error)
	ResendInvite(ctx context.Context, id uuid.UUID) (auth.Invite, error)
	RevokeInvite(ctx context.Context, id uuid.UUID) error
	AdminList(ctx context.Context) ([]auth.ManagedUser, []auth.Invite, error)
	DisableUser(ctx context.Context, id uuid.UUID) error
	EnableUser(ctx context.Context, id uuid.UUID) error
	MakeAdmin(ctx context.Context, id uuid.UUID) error
}

var _ UserAdmin = (*auth.Store)(nil)

// adminOnly answers 404 to anyone but an Admin, so the routes behind it
// cannot be told apart from routes that do not exist (auth-keys.md §4.1).
func (a *authHandlers) adminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, _ := auth.UserFrom(r.Context()); !u.IsAdmin {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type inviteJSON struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func toInviteJSON(inv auth.Invite) inviteJSON {
	return inviteJSON{ID: inv.ID, Email: inv.Email, ExpiresAt: inv.ExpiresAt, CreatedAt: inv.CreatedAt}
}

// handleCreateInvite opens an Invite and emails it. Inviting an email that
// already has an open Invite re-sends that one (200 instead of 201).
func (a *authHandlers) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	var req emailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	addr, err := netmail.ParseAddress(req.Email)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid email")
		return
	}
	u, _ := auth.UserFrom(r.Context())
	inv, created, err := a.cfg.Admin.CreateInvite(r.Context(), u.ID, addr.Address)
	if errors.Is(err, auth.ErrAlreadyUser) {
		writeError(w, http.StatusConflict, "already a user")
		return
	}
	if err != nil {
		a.logger.Error("create invite", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create invite")
		return
	}
	a.logger.Info("invite created", "actor_id", u.ID, "invite_id", inv.ID, "email", inv.Email)
	if !a.sendInvite(w, r, inv) {
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, toInviteJSON(inv))
}

func (a *authHandlers) handleResendInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r, "invite not found")
	if !ok {
		return
	}
	inv, err := a.cfg.Admin.ResendInvite(r.Context(), id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invite not found")
		return
	}
	if err != nil {
		a.logger.Error("resend invite", "error", err)
		writeError(w, http.StatusInternalServerError, "could not resend invite")
		return
	}
	if a.sendInvite(w, r, inv) {
		w.WriteHeader(http.StatusNoContent)
	}
}

// sendInvite emails inv and reports whether it went out; if not, it has
// already answered 502. The Invite stays, so the Admin can Resend.
func (a *authHandlers) sendInvite(w http.ResponseWriter, r *http.Request, inv auth.Invite) bool {
	ctx, cancel := context.WithTimeout(r.Context(), codeSendTimeout)
	defer cancel()
	text := "You've been invited to jelly-fish.\n\n" +
		"Sign in at " + a.cfg.PublicURL + " with this email address; you'll get a code by email.\n\n" +
		"The invite expires in " + strconv.Itoa(int(auth.InviteLifetime.Hours()/24)) + " days.\n"
	if err := a.cfg.Mailer.Send(ctx, inv.Email, "You're invited to jelly-fish", text); err != nil {
		a.logger.Error("send invite", "error", err)
		writeError(w, http.StatusBadGateway, "invite saved but the email could not be sent; try Resend")
		return false
	}
	return true
}

func (a *authHandlers) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := adminPathID(w, r, "invite not found")
	if !ok {
		return
	}
	err := a.cfg.Admin.RevokeInvite(r.Context(), id)
	if errors.Is(err, auth.ErrNotFound) {
		writeError(w, http.StatusNotFound, "invite not found")
		return
	}
	if err != nil {
		a.logger.Error("revoke invite", "error", err)
		writeError(w, http.StatusInternalServerError, "could not revoke invite")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type managedUserJSON struct {
	ID         uuid.UUID  `json:"id"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	IsAdmin    bool       `json:"is_admin"`
	DisabledAt *time.Time `json:"disabled_at"`
	CreatedAt  time.Time  `json:"created_at"`
}

func (a *authHandlers) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, invites, err := a.cfg.Admin.AdminList(r.Context())
	if err != nil {
		a.logger.Error("list users", "error", err)
		writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	out := struct {
		Users   []managedUserJSON `json:"users"`
		Invites []inviteJSON      `json:"invites"`
	}{Users: make([]managedUserJSON, len(users)), Invites: make([]inviteJSON, len(invites))}
	for i, m := range users {
		out.Users[i] = managedUserJSON{
			ID: m.ID, Email: m.Email, Name: m.Name, IsAdmin: m.IsAdmin, DisabledAt: m.DisabledAt, CreatedAt: m.CreatedAt,
		}
	}
	for i, inv := range invites {
		out.Invites[i] = toInviteJSON(inv)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *authHandlers) handleDisableUser(w http.ResponseWriter, r *http.Request) {
	a.userAction(w, r, "user disabled", a.cfg.Admin.DisableUser)
}

func (a *authHandlers) handleEnableUser(w http.ResponseWriter, r *http.Request) {
	a.userAction(w, r, "user enabled", a.cfg.Admin.EnableUser)
}

func (a *authHandlers) handleMakeAdmin(w http.ResponseWriter, r *http.Request) {
	a.userAction(w, r, "user made admin", a.cfg.Admin.MakeAdmin)
}

// userAction runs one Admin action on the User in the path and logs it as
// event on success.
func (a *authHandlers) userAction(w http.ResponseWriter, r *http.Request, event string, act func(context.Context, uuid.UUID) error) {
	id, ok := adminPathID(w, r, "user not found")
	if !ok {
		return
	}
	err := act(r.Context(), id)
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, "user not found")
	case errors.Is(err, auth.ErrLastAdmin):
		writeError(w, http.StatusConflict, "there must be at least one active admin")
	case err != nil:
		a.logger.Error("update user", "action", event, "error", err)
		writeError(w, http.StatusInternalServerError, "could not update user")
	default:
		actor, _ := auth.UserFrom(r.Context())
		a.logger.Info(event, "actor_id", actor.ID, "user_id", id)
		w.WriteHeader(http.StatusNoContent)
	}
}

// adminPathID parses the {id} path value, answering 404 with notFound if it is
// not a UUID.
func adminPathID(w http.ResponseWriter, r *http.Request, notFound string) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, notFound)
		return uuid.UUID{}, false
	}
	return id, true
}
