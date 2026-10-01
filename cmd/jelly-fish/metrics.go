package main

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// mountMetrics serves reg at GET /metrics on srv, next to its existing routes.
func mountMetrics(srv *http.Server, reg *prometheus.Registry) {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))
	mux.Handle("/", srv.Handler)
	srv.Handler = mux
}
