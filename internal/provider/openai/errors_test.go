package openai

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func errBody(typ, code, msg string) string {
	return `{"error":{"type":"` + typ + `","code":"` + code + `","message":"` + msg + `","param":null}}`
}

func streamErr(t *testing.T, c Client) *provider.Error {
	t.Helper()
	_, err := c.Provider("sk-canary").Stream(t.Context(), userHi(), func(provider.Delta) {})
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *provider.Error", err)
	}
	if strings.Contains(pe.Error(), "sk-canary") {
		t.Fatalf("error carries the key: %v", pe)
	}
	return pe
}

func TestErrorFixturesMapToClasses(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		retryAfter string
		body       string
		want       provider.ErrorKind
		wantWait   time.Duration
	}{
		{"401", 401, "", errBody("invalid_request_error", "invalid_api_key", "Incorrect API key"), provider.KindKeyInvalid, 0},
		{"403", 403, "", errBody("invalid_request_error", "", "not allowed"), provider.KindKeyInvalid, 0},
		{"429 quota", 429, "", errBody("insufficient_quota", "insufficient_quota", "You exceeded your current quota"), provider.KindBilling, 0},
		{"429 without retry-after", 429, "", errBody("requests", "rate_limit_exceeded", "slow down"), provider.KindRateLimited, 0},
		{"429 short retry-after", 429, "20", errBody("requests", "rate_limit_exceeded", "slow down"), provider.KindRateLimited, 20 * time.Second},
		{"429 long retry-after", 429, "120", errBody("requests", "rate_limit_exceeded", "slow down"), provider.KindLongWait, 120 * time.Second},
		{"404", 404, "", errBody("invalid_request_error", "model_not_found", "The model does not exist"), provider.KindModelUnavailable, 0},
		{"413", 413, "", errBody("invalid_request_error", "", "too big"), provider.KindTooLarge, 0},
		{"400", 400, "", errBody("invalid_request_error", "invalid_value", "bad input"), provider.KindBug, 0},
		{"500", 500, "", errBody("server_error", "", "internal"), provider.KindProviderDown, 0},
		{"503", 503, "", errBody("server_error", "", "overloaded"), provider.KindProviderDown, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("X-Request-Id", "req_err")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)
			pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)})
			if pe.Kind != tc.want || pe.RetryAfter != tc.wantWait || pe.RequestID != "req_err" {
				t.Fatalf("error = %+v, want kind %s, wait %v, request req_err", pe, tc.want, tc.wantWait)
			}
		})
	}
}

func TestStreamErrorsMapToClasses(t *testing.T) {
	tests := []struct {
		fixture string
		want    provider.ErrorKind
	}{
		{"failed.sse", provider.KindProviderDown},
		{"error_event.sse", provider.KindBilling},
		{"cut.sse", provider.KindProviderDown},
	}
	for _, tc := range tests {
		t.Run(tc.fixture, func(t *testing.T) {
			c, _ := sseServer(t, tc.fixture)
			if pe := streamErr(t, c); pe.Kind != tc.want || pe.RequestID != "req_fixture" {
				t.Fatalf("error = %+v, want %s", pe, tc.want)
			}
		})
	}
}

func TestFailedResponseKeepsWhatItConsumed(t *testing.T) {
	c, _ := sseServer(t, "failed.sse")
	if pe := streamErr(t, c); pe.Usage != (provider.Usage{Input: 42, Output: 7}) {
		t.Fatalf("usage = %+v, want input 42 output 7", pe.Usage)
	}
}

func TestIdleStreamIsProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("X-Request-Id", "req_idle")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t), IdleTimeout: 100 * time.Millisecond})
	if pe.Kind != provider.KindProviderDown || !errors.Is(pe, provider.ErrStreamIdle) || pe.RequestID != "req_idle" {
		t.Fatalf("err = %+v, want provider_down wrapping ErrStreamIdle, request req_idle", pe)
	}
}

func TestStreamDoesNotRetry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(errBody("server_error", "", "overloaded")))
	}))
	t.Cleanup(srv.Close)
	if pe := streamErr(t, Client{BaseURL: srv.URL}); pe.Kind != provider.KindProviderDown {
		t.Fatalf("error = %+v", pe)
	}
	if n := hits.Load(); n != 1 {
		t.Fatalf("%d attempts, want 1", n)
	}
}
