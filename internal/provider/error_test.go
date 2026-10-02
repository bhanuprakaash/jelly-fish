package provider_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

func TestIdleTimeoutIsFiveMinutes(t *testing.T) {
	if provider.IdleTimeout != 300*time.Second {
		t.Fatalf("IdleTimeout = %v, want 300s", provider.IdleTimeout)
	}
}

func TestErrorUnwrapsToItsCause(t *testing.T) {
	err := error(&provider.Error{Kind: provider.KindProviderDown, RequestID: "req_1", Err: provider.ErrStreamIdle})
	if !errors.Is(err, provider.ErrStreamIdle) {
		t.Fatal("Error does not unwrap to its cause")
	}
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Kind != provider.KindProviderDown {
		t.Fatalf("errors.As = %+v", pe)
	}
}

func TestRetryableKinds(t *testing.T) {
	tests := []struct {
		kind provider.ErrorKind
		want bool
	}{
		{provider.KindRateLimited, true},
		{provider.KindProviderDown, true},
		{provider.KindLongWait, false},
		{provider.KindKeyInvalid, false},
		{provider.KindBilling, false},
		{provider.KindModelUnavailable, false},
		{provider.KindTooLarge, false},
		{provider.KindBug, false},
	}
	for _, tc := range tests {
		t.Run(string(tc.kind), func(t *testing.T) {
			if got := tc.kind.Retryable(); got != tc.want {
				t.Fatalf("Retryable = %v, want %v", got, tc.want)
			}
		})
	}
}

func stalledServer(t *testing.T, writes int, gap time.Duration) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range writes {
			_, _ = w.Write([]byte("event: ping\n\n"))
			w.(http.Flusher).Flush()
			time.Sleep(gap)
		}
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWatchIdleClosesAStalledBody(t *testing.T) {
	srv := stalledServer(t, 1, 0)
	client := &http.Client{Transport: provider.WatchIdle(http.DefaultTransport, 100*time.Millisecond)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	start := time.Now()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, provider.ErrStreamIdle) {
		t.Fatalf("read err = %v, want ErrStreamIdle", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %v to notice the stall", time.Since(start))
	}
}

func TestWatchIdleResetsOnEveryRead(t *testing.T) {
	srv := stalledServer(t, 8, 40*time.Millisecond)
	client := &http.Client{Transport: provider.WatchIdle(http.DefaultTransport, 150*time.Millisecond)}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	buf := make([]byte, 13)
	for range 8 {
		if _, err := io.ReadFull(resp.Body, buf); err != nil {
			t.Fatalf("read err = %v, want the stream to stay alive while bytes flow", err)
		}
	}
}
