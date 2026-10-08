package gemini

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func errBody(code int, status, message string, details ...string) string {
	return `{"error":{"code":` + strconv.Itoa(code) + `,"message":"` + message + `","status":"` + status + `","details":[` + strings.Join(details, ",") + `]}}`
}

func retryInfo(delay string) string {
	return `{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"` + delay + `"}`
}

const badKey = `{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"API_KEY_INVALID","domain":"googleapis.com"}`

func streamErr(t *testing.T, c Client) *provider.Error {
	t.Helper()
	_, err := c.Provider("AIza-canary").Stream(t.Context(), userHi(), func(provider.Delta) {})
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *provider.Error", err)
	}
	if strings.Contains(pe.Error(), "AIza-canary") {
		t.Fatalf("error carries the key: %v", pe)
	}
	return pe
}

func TestErrorFixturesMapToClasses(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		want     provider.ErrorKind
		wantWait time.Duration
	}{
		{"400 bad key", 400, errBody(400, "INVALID_ARGUMENT", "API key not valid.", badKey), provider.KindKeyInvalid, 0},
		{"401", 401, errBody(401, "UNAUTHENTICATED", "bad credentials"), provider.KindKeyInvalid, 0},
		{"403", 403, errBody(403, "PERMISSION_DENIED", "not allowed"), provider.KindKeyInvalid, 0},
		{"402", 402, errBody(402, "", "Prepay credit is depleted"), provider.KindBilling, 0},
		{"400 billing off", 400, errBody(400, "FAILED_PRECONDITION", "enable billing"), provider.KindBilling, 0},
		{"429 without retry delay", 429, errBody(429, "RESOURCE_EXHAUSTED", "slow down"), provider.KindRateLimited, 0},
		{"429 short retry delay", 429, errBody(429, "RESOURCE_EXHAUSTED", "slow down", retryInfo("20s")), provider.KindRateLimited, 20 * time.Second},
		{"429 delay of 60 s", 429, errBody(429, "RESOURCE_EXHAUSTED", "slow down", retryInfo("60s")), provider.KindRateLimited, 60 * time.Second},
		{"429 long retry delay", 429, errBody(429, "RESOURCE_EXHAUSTED", "slow down", retryInfo("120.5s")), provider.KindLongWait, 120500 * time.Millisecond},
		{"404", 404, errBody(404, "NOT_FOUND", "model not found"), provider.KindModelUnavailable, 0},
		{"413", 413, errBody(413, "", "too big"), provider.KindTooLarge, 0},
		{"400 bad request", 400, errBody(400, "INVALID_ARGUMENT", "bad input"), provider.KindBug, 0},
		{"500", 500, errBody(500, "INTERNAL", "internal"), provider.KindProviderDown, 0},
		{"503", 503, errBody(503, "UNAVAILABLE", "overloaded"), provider.KindProviderDown, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)})
			if pe.Kind != tc.want || pe.RetryAfter != tc.wantWait {
				t.Fatalf("error = %+v, want kind %s, wait %v", pe, tc.want, tc.wantWait)
			}
		})
	}
}

func TestCutStreamKeepsItsRequestIDAndIsProviderDown(t *testing.T) {
	c, _ := sseServer(t, "cut.sse")
	if pe := streamErr(t, c); pe.Kind != provider.KindProviderDown || pe.RequestID != "resp_cut" {
		t.Fatalf("error = %+v, want provider_down with request resp_cut", pe)
	}
}

func TestInBandErrorIsClassifiedLikeItsStatus(t *testing.T) {
	c, _ := sseServer(t, "error_event.sse")
	if pe := streamErr(t, c); pe.Kind != provider.KindLongWait || pe.RetryAfter != 120*time.Second {
		t.Fatalf("error = %+v, want long_wait of 120s", pe)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(errBody(429, "RESOURCE_EXHAUSTED", "slow down", retryInfo("120s")) + "\n\n"))
	}))
	t.Cleanup(srv.Close)
	if pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)}); pe.Kind != provider.KindLongWait {
		t.Fatalf("no-prefix error = %+v, want long_wait", pe)
	}
}

func TestIdleStreamIsProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t), IdleTimeout: 100 * time.Millisecond})
	if pe.Kind != provider.KindProviderDown || !errors.Is(pe, provider.ErrStreamIdle) {
		t.Fatalf("err = %+v, want provider_down wrapping ErrStreamIdle", pe)
	}
}

func TestStreamDoesNotRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(errBody(503, "UNAVAILABLE", "overloaded")))
	}))
	t.Cleanup(srv.Close)
	if pe := streamErr(t, Client{BaseURL: srv.URL}); pe.Kind != provider.KindProviderDown {
		t.Fatalf("error = %+v", pe)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d attempts, want 1", n)
	}
}
