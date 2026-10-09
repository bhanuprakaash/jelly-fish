package anthropic

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func errBody(typ, msg string) string {
	return `{"type":"error","error":{"type":"` + typ + `","message":"` + msg + `"},"request_id":"req_body"}`
}

func streamErr(t *testing.T, c Client) *provider.Error {
	t.Helper()
	_, err := c.Provider("sk-ant-canary").Stream(t.Context(), userHi(), func(provider.Delta) {})
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want *provider.Error", err)
	}
	if strings.Contains(pe.Error(), "sk-ant-canary") {
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
		{"401", 401, "", errBody("authentication_error", "invalid x-api-key"), provider.KindKeyInvalid, 0},
		{"403", 403, "", errBody("permission_error", "not allowed"), provider.KindKeyInvalid, 0},
		{"402", 402, "", errBody("billing_error", "payment required"), provider.KindBilling, 0},
		{"400 credit balance", 400, "", errBody("invalid_request_error", "Your credit balance is too low to access the Anthropic API."), provider.KindBilling, 0},
		{"400 workspace spend limit", 400, "", errBody("invalid_request_error", "You have reached your specified workspace API usage limits."), provider.KindBilling, 0},
		{"429 spend cap without retry-after", 429, "", errBody("rate_limit_error", "Monthly spend cap reached"), provider.KindBilling, 0},
		{"429 short retry-after", 429, "30", errBody("rate_limit_error", "slow down"), provider.KindRateLimited, 30 * time.Second},
		{"429 retry-after at the cap", 429, "60", errBody("rate_limit_error", "slow down"), provider.KindRateLimited, 60 * time.Second},
		{"429 long retry-after", 429, "120", errBody("rate_limit_error", "slow down"), provider.KindLongWait, 120 * time.Second},
		{"404", 404, "", errBody("not_found_error", "model: nope"), provider.KindModelUnavailable, 0},
		{"413", 413, "", errBody("request_too_large", "too big"), provider.KindTooLarge, 0},
		{"400 prefix binding", 400, "", errBody("invalid_request_error", "messages.1.content.0: thinking blocks cannot be modified"), provider.KindBug, 0},
		{"400 block_binding", 400, "", errBody("invalid_request_error", "block_binding mismatch"), provider.KindBug, 0},
		{"400 tool_choice", 400, "", errBody("invalid_request_error", "tool_choice is not supported with thinking"), provider.KindBug, 0},
		{"400 other", 400, "", errBody("invalid_request_error", "max_tokens: too small"), provider.KindBug, 0},
		{"422", 422, "", errBody("invalid_request_error", "unprocessable"), provider.KindBug, 0},
		{"500", 500, "", errBody("api_error", "internal"), provider.KindProviderDown, 0},
		{"504", 504, "", errBody("timeout_error", "timeout"), provider.KindProviderDown, 0},
		{"529 with retry-after", 529, "7", errBody("overloaded_error", "Overloaded"), provider.KindProviderDown, 7 * time.Second},
		{"529 long retry-after", 529, "3600", errBody("overloaded_error", "Overloaded"), provider.KindLongWait, time.Hour},
		{"599 unknown 5xx", 599, "", `not json`, provider.KindProviderDown, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Request-Id", "req_header")
				if tc.retryAfter != "" {
					w.Header().Set("Retry-After", tc.retryAfter)
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			t.Cleanup(srv.Close)

			pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)})
			if pe.Kind != tc.want || pe.RetryAfter != tc.wantWait || pe.RequestID != "req_header" || pe.HTTPStatus != tc.status {
				t.Fatalf("got kind=%s wait=%v request=%q status=%d, want kind=%s wait=%v request=req_header status=%d", pe.Kind, pe.RetryAfter, pe.RequestID, pe.HTTPStatus, tc.want, tc.wantWait, tc.status)
			}
		})
	}
}

const startEvent = "event: message_start\n" +
	`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":42,"output_tokens":1}}}` + "\n\n"

func TestMidStreamErrorsMapByType(t *testing.T) {
	tests := []struct {
		typ  string
		want provider.ErrorKind
	}{
		{"overloaded_error", provider.KindProviderDown},
		{"api_error", provider.KindProviderDown},
		{"timeout_error", provider.KindProviderDown},
		{"rate_limit_error", provider.KindRateLimited},
		{"authentication_error", provider.KindKeyInvalid},
		{"billing_error", provider.KindBilling},
		{"unheard_of_error", provider.KindBug},
	}
	for _, tc := range tests {
		t.Run(tc.typ, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Request-Id", "req_mid")
				_, _ = w.Write([]byte(startEvent + "event: error\ndata: " + errBody(tc.typ, "boom") + "\n\n"))
			}))
			t.Cleanup(srv.Close)

			pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)})
			if pe.Kind != tc.want || pe.RequestID != "req_mid" {
				t.Fatalf("got kind=%s request=%q, want kind=%s request=req_mid", pe.Kind, pe.RequestID, tc.want)
			}
			if pe.Usage.Input != 42 {
				t.Fatalf("usage = %+v, want the message_start input tokens", pe.Usage)
			}
		})
	}
}

func TestStalledStreamIsProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Request-Id", "req_idle")
		_, _ = w.Write([]byte(startEvent))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)

	pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t), IdleTimeout: 100 * time.Millisecond})
	if pe.Kind != provider.KindProviderDown || !errors.Is(pe, provider.ErrStreamIdle) {
		t.Fatalf("err = %v, want provider_down wrapping ErrStreamIdle", pe)
	}
	if pe.Usage.Input != 42 || pe.RequestID != "req_idle" {
		t.Fatalf("usage = %+v request = %q", pe.Usage, pe.RequestID)
	}
}

func TestDroppedConnectionIsProviderDown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	t.Cleanup(srv.Close)

	pe := streamErr(t, Client{BaseURL: srv.URL, Catalog: testCatalog(t)})
	if pe.Kind != provider.KindProviderDown || pe.HTTPStatus != 0 {
		t.Fatalf("kind = %s status = %d, want provider_down with no status", pe.Kind, pe.HTTPStatus)
	}
}

func TestCancelledStreamIsNotClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(startEvent))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	c := Client{BaseURL: srv.URL, Catalog: testCatalog(t)}
	_, err := c.Provider("k").Stream(ctx, userHi(), func(provider.Delta) {})
	var pe *provider.Error
	if err == nil || errors.As(err, &pe) {
		t.Fatalf("err = %v, want the bare context error", err)
	}
}
