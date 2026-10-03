package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

type route struct{ method, path string }

// stateChangingRoutes lists every non-GET API route, signed-in or not.
func stateChangingRoutes() []route {
	return []route{
		{http.MethodPost, "/api/auth/code"},
		{http.MethodPost, "/api/auth/code/verify"},
		{http.MethodPost, "/api/auth/link"},
		{http.MethodPost, "/api/auth/logout"},
		{http.MethodPost, "/api/auth/logout-all"},
		{http.MethodDelete, "/api/me/login-sessions/" + uuid.NewString()},
		{http.MethodPut, "/api/provider-keys/anthropic"},
		{http.MethodDelete, "/api/provider-keys/anthropic"},
		{http.MethodPost, "/api/sessions"},
		{http.MethodPost, "/api/sessions/" + uuid.NewString() + "/messages"},
		{http.MethodPost, "/api/admin/invites"},
		{http.MethodPost, "/api/admin/invites/" + uuid.NewString() + "/resend"},
		{http.MethodDelete, "/api/admin/invites/" + uuid.NewString()},
		{http.MethodPost, "/api/admin/users/" + uuid.NewString() + "/disable"},
		{http.MethodPost, "/api/admin/users/" + uuid.NewString() + "/enable"},
		{http.MethodPost, "/api/admin/users/" + uuid.NewString() + "/make-admin"},
	}
}

func TestCrossSiteStateChange(t *testing.T) {
	for _, rt := range stateChangingRoutes() {
		t.Run(rt.method+" "+rt.path, func(t *testing.T) {
			tests := []struct {
				name    string
				headers map[string]string
				reject  bool
			}{
				{"cross-site", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
				{"same-site sibling", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
				{"cross origin without fetch metadata", map[string]string{"Origin": "https://evil.example"}, true},
				{"same-origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
				{"origin matches host", map[string]string{"Origin": "http://example.com"}, false},
				{"non-browser client", nil, false},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					srv := newTestServer(t, &fakeRepo{})

					rr := httptest.NewRecorder()
					req := signIn(httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
					for k, v := range tt.headers {
						req.Header.Set(k, v)
					}
					srv.Handler.ServeHTTP(rr, req)

					if got := rr.Code == http.StatusForbidden; got != tt.reject {
						t.Fatalf("status = %d, want rejected = %v", rr.Code, tt.reject)
					}
				})
			}
		})
	}
}

func TestCrossSiteStream(t *testing.T) {
	tests := []struct {
		name       string
		fetchSite  string
		wantStatus int
	}{
		{"cross-site", "cross-site", http.StatusForbidden},
		{"same-origin", "same-origin", http.StatusOK},
		{"typed into the address bar", "none", http.StatusOK},
		{"non-browser client", "", http.StatusOK},
	}
	for _, path := range []string{"/api/sessions/" + uuid.NewString() + "/events", "/api/activity"} {
		for _, tt := range tests {
			t.Run(path+"/"+tt.name, func(t *testing.T) {
				srv := newTestServer(t, &fakeRepo{})
				ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
				defer cancel()

				rr := httptest.NewRecorder()
				req := signIn(httptest.NewRequestWithContext(ctx, http.MethodGet, path, nil))
				if tt.fetchSite != "" {
					req.Header.Set("Sec-Fetch-Site", tt.fetchSite)
				}
				srv.Handler.ServeHTTP(rr, req)

				if rr.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d", rr.Code, tt.wantStatus)
				}
			})
		}
	}
}

func TestNoCORSHeaders(t *testing.T) {
	requests := []route{
		{http.MethodOptions, "/api/sessions"},
		{http.MethodOptions, "/api/auth/code"},
		{http.MethodGet, "/api/me"},
		{http.MethodGet, "/"},
	}
	for _, rt := range append(requests, stateChangingRoutes()...) {
		for _, origin := range []string{"https://evil.example", "http://example.com"} {
			t.Run(rt.method+" "+rt.path+" from "+origin, func(t *testing.T) {
				srv := newTestServer(t, &fakeRepo{})

				rr := httptest.NewRecorder()
				req := signIn(httptest.NewRequest(rt.method, rt.path, strings.NewReader("{}")))
				req.Header.Set("Origin", origin)
				req.Header.Set("Access-Control-Request-Method", http.MethodPost)
				srv.Handler.ServeHTTP(rr, req)

				for k := range rr.Header() {
					if strings.HasPrefix(k, "Access-Control-") {
						t.Fatalf("response carries %s", k)
					}
				}
			})
		}
	}
}
