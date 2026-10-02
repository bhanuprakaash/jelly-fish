package telemetry_test

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/bhanuprakaash/jelly-fish/internal/telemetry"
)

// clearOTelEnv blanks the vars Setup reads so the host's own do not leak in.
func clearOTelEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_TRACES_EXPORTER", "OTEL_SDK_DISABLED",
	} {
		t.Setenv(k, "")
	}
}

func receiver(t *testing.T) *atomic.Int32 {
	t.Helper()
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/traces" {
			posts.Add(1)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", srv.URL)
	return &posts
}

func TestSetupExportsOnlyWhenConfigured(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		noReceive bool
		wantPosts int32
	}{
		{name: "endpoint set", wantPosts: 1},
		{name: "no endpoint", noReceive: true},
		{name: "traces exporter none", env: map[string]string{"OTEL_TRACES_EXPORTER": "none"}},
		{name: "sdk disabled", env: map[string]string{"OTEL_SDK_DISABLED": "TRUE"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearOTelEnv(t)
			posts := receiver(t)
			if tt.noReceive {
				t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			tp, shutdown, err := telemetry.Setup(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			_, span := tp.Tracer("t").Start(t.Context(), "x")
			span.End()
			if err := shutdown(t.Context()); err != nil {
				t.Fatal(err)
			}

			if n := posts.Load(); n != tt.wantPosts {
				t.Errorf("%d exports to /v1/traces, want %d", n, tt.wantPosts)
			}
		})
	}
}
