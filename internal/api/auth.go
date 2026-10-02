package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	netmail "net/mail"
	"sync"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/auth"
	"github.com/bhanuprakaash/jelly-fish/internal/mail"
)

const (
	// loginCookie is the production cookie name; the __Host- prefix makes
	// browsers require Secure, Path=/ and no Domain (auth-keys.md §4.2).
	loginCookie = "__Host-jf_login"
	// devLoginCookie is the cookie name when cookies may go over plain http.
	devLoginCookie  = "jf_login"
	loginCookieAge  = 30 * 24 * time.Hour
	codeSendTimeout = 15 * time.Second
	codeAcceptedMsg = "If you're invited, a code is on its way."
)

// Authenticator is what the auth endpoints and middleware need from the login
// store (internal/auth.Store satisfies it).
type Authenticator interface {
	RequestCode(ctx context.Context, email string) (string, error)
	VerifyCode(ctx context.Context, email, code, userAgent string) (string, error)
	Authenticate(ctx context.Context, token string) (auth.User, error)
}

var _ Authenticator = (*auth.Store)(nil)

// Mailer sends one email (notifications.md §4.1); html may be empty.
// internal/mail.SMTP satisfies it.
type Mailer interface {
	Send(ctx context.Context, to, subject, text, html string) error
}

var _ Mailer = (*mail.SMTP)(nil)

// AuthConfig wires login into the server.
type AuthConfig struct {
	Authenticator Authenticator
	Mailer        Mailer
	// InsecureCookie sends the cookie without Secure under the name jf_login,
	// for local http only.
	InsecureCookie bool
}

type authHandlers struct {
	cfg    AuthConfig
	logger *slog.Logger
	// bg tracks in-flight code emails; each stops at codeSendTimeout.
	bg sync.WaitGroup
}

func (a *authHandlers) cookieName() string {
	if a.cfg.InsecureCookie {
		return devLoginCookie
	}
	return loginCookie
}

type emailRequest struct {
	Email string `json:"email"`
}

// handleRequestCode answers 202 whether or not email belongs to a User, so it
// cannot be used to probe for Users. The email goes out in the background so
// the response time does not differ either.
func (a *authHandlers) handleRequestCode(w http.ResponseWriter, r *http.Request) {
	var req emailRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// An address that can't be parsed gets the same answer, and no email.
	if addr, err := netmail.ParseAddress(req.Email); err == nil {
		code, err := a.cfg.Authenticator.RequestCode(r.Context(), addr.Address)
		if err != nil {
			a.logger.Error("request login code", "error", err)
		} else if code != "" {
			a.sendCode(context.WithoutCancel(r.Context()), auth.NormalizeEmail(addr.Address), code)
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"message": codeAcceptedMsg})
}

// sendCode emails the code in the background, outliving the request.
func (a *authHandlers) sendCode(ctx context.Context, email, code string) {
	a.bg.Go(func() {
		ctx, cancel := context.WithTimeout(ctx, codeSendTimeout)
		defer cancel()
		subject := "Your jelly-fish code: " + code
		text := "Your jelly-fish sign-in code is " + code + ".\nIt expires in 10 minutes. If you didn't ask for it, ignore this email.\n"
		if err := a.cfg.Mailer.Send(ctx, email, subject, text, ""); err != nil {
			a.logger.Error("send login code", "error", err)
		}
	})
}

type verifyRequest struct {
	Email string `json:"email"`
	Code  string `json:"code"`
}

func (a *authHandlers) handleVerifyCode(w http.ResponseWriter, r *http.Request) {
	var req verifyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	token, err := a.cfg.Authenticator.VerifyCode(r.Context(), req.Email, req.Code, r.UserAgent())
	if errors.Is(err, auth.ErrInvalidCode) {
		a.logger.Info("login failed", "email", auth.NormalizeEmail(req.Email), "method", "code")
		writeError(w, http.StatusUnauthorized, "invalid or expired code")
		return
	}
	if err != nil {
		a.logger.Error("verify login code", "error", err)
		writeError(w, http.StatusInternalServerError, "could not sign in")
		return
	}
	a.logger.Info("login succeeded", "email", auth.NormalizeEmail(req.Email), "method", "code")
	http.SetCookie(w, &http.Cookie{
		Name:     a.cookieName(),
		Value:    token,
		Path:     "/",
		MaxAge:   int(loginCookieAge.Seconds()),
		Secure:   !a.cfg.InsecureCookie,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

// authn lets a request through only with a valid Login Session cookie,
// handing next the User (auth-keys.md §4.3).
func (a *authHandlers) authn(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(a.cookieName())
		if err != nil {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		u, err := a.cfg.Authenticator.Authenticate(r.Context(), c.Value)
		if errors.Is(err, auth.ErrUnauthenticated) {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if err != nil {
			a.logger.Error("authenticate", "error", err)
			writeError(w, http.StatusInternalServerError, "could not check sign-in")
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), u)))
	})
}

func handleMe(w http.ResponseWriter, r *http.Request) {
	u, _ := auth.UserFrom(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "email": u.Email, "name": u.Name, "is_admin": u.IsAdmin})
}
