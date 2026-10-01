package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/bhanuprakaash/jelly-fish/internal/stream"
)

func TestMountMetricsServesStreamMetricsBesideExistingRoutes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m, err := stream.NewMetrics(reg)
	if err != nil {
		t.Fatal(err)
	}
	m.Refusals.Inc()
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "health")
	})}
	mountMetrics(srv, reg)

	tests := []struct {
		path string
		want string
	}{
		{"/metrics", "jf_stream_refusals_total 1"},
		{"/readyz", "health"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			rr := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
			}
			if !strings.Contains(rr.Body.String(), tt.want) {
				t.Errorf("body = %q, want it to contain %q", rr.Body.String(), tt.want)
			}
		})
	}
}
