// Package telemetry builds the process's OpenTelemetry tracer provider from
// the standard OTEL_* environment variables.
package telemetry

import (
	"context"
	"fmt"
	"os"
	"strings"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

// Setup returns a tracer provider that exports over OTLP/HTTP, which reads
// its endpoint, headers and the rest from OTEL_EXPORTER_OTLP_*. With no
// endpoint set, or OTEL_TRACES_EXPORTER=none or OTEL_SDK_DISABLED=true, it
// returns a no-op provider. shutdown flushes pending spans; call it once
// before exit.
func Setup(ctx context.Context) (tp trace.TracerProvider, shutdown func(context.Context) error, err error) {
	if !enabled() {
		return noop.NewTracerProvider(), func(context.Context) error { return nil }, nil
	}
	exp, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("create otlp exporter: %w", err)
	}
	sdk := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	return sdk, sdk.Shutdown, nil
}

func enabled() bool {
	if strings.EqualFold(os.Getenv("OTEL_SDK_DISABLED"), "true") || os.Getenv("OTEL_TRACES_EXPORTER") == "none" {
		return false
	}
	return os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || os.Getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != ""
}
