// Package tracing wraps a Provider so each call opens one OpenTelemetry span
// (agent-loop.md §4.5). A span carries ids, counts and error classes, never a
// key or message content.
package tracing

import (
	"context"
	"errors"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

const tracerName = "github.com/bhanuprakaash/jelly-fish/internal/provider"

// IDs ties a span to the session turn that made the call.
type IDs struct {
	SessionID string
	TurnID    string
}

// Wrap returns a Provider that records one span per Stream call on tp. Put it
// below the retry wrapper so each attempt gets its own span.
func Wrap(p provider.Provider, tp trace.TracerProvider, ids IDs) provider.Provider {
	return traced{p: p, tracer: tp.Tracer(tracerName), ids: ids}
}

var _ provider.Provider = traced{}

type traced struct {
	p      provider.Provider
	tracer trace.Tracer
	ids    IDs
}

func (t traced) Name() string { return t.p.Name() }

func (t traced) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	ctx, span := t.tracer.Start(ctx, "llm.call", trace.WithAttributes(
		attribute.String("gen_ai.provider.name", t.p.Name()),
		attribute.String("gen_ai.request.model", req.Model),
		attribute.String("jf.session_id", t.ids.SessionID),
		attribute.String("jf.turn_id", t.ids.TurnID),
	))
	defer span.End()

	resp, err := t.p.Stream(ctx, req, onDelta)
	if err == nil {
		setRequestID(span, resp.RequestID)
		span.SetAttributes(usageAttrs(resp.Usage)...)
		span.SetAttributes(attribute.StringSlice("gen_ai.response.finish_reasons", []string{string(resp.StopReason)}))
		return resp, nil
	}
	var pe *provider.Error
	if !errors.As(err, &pe) {
		span.SetStatus(codes.Error, "error")
		return resp, err
	}
	setRequestID(span, pe.RequestID)
	// A failure that consumed nothing carries no usage, not zeros.
	if pe.Usage != (provider.Usage{}) {
		span.SetAttributes(usageAttrs(pe.Usage)...)
	}
	if pe.HTTPStatus != 0 {
		span.SetAttributes(attribute.Int("http.response.status_code", pe.HTTPStatus))
	}
	span.SetAttributes(attribute.String("jf.error_class", string(pe.Kind)))
	span.SetStatus(codes.Error, string(pe.Kind))
	return resp, err
}

func setRequestID(span trace.Span, id string) {
	if id != "" {
		span.SetAttributes(attribute.String("jf.request_id", id))
	}
}

func usageAttrs(u provider.Usage) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.Int64("gen_ai.usage.input_tokens", u.Input),
		attribute.Int64("gen_ai.usage.cache_read_tokens", u.CacheRead),
		attribute.Int64("gen_ai.usage.cache_write_5m_tokens", u.CacheWrite5m),
		attribute.Int64("gen_ai.usage.cache_write_1h_tokens", u.CacheWrite1h),
		attribute.Int64("gen_ai.usage.output_tokens", u.Output),
		attribute.Int64("gen_ai.usage.reasoning_tokens", u.Reasoning),
	}
}
