// Package tracing wraps a Tool so each call opens one OpenTelemetry span
// (agent-loop.md §4.5). A span carries ids, the tool name and whether the call
// failed, never arguments or results.
package tracing

import (
	"context"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

const tracerName = "github.com/bhanuprakaash/jelly-fish/internal/tool"

// IDs ties a span to the session turn whose reply asked for the call.
type IDs struct {
	SessionID string
	TurnID    string
}

// Wrap returns a Tool that records one span per Call on tp.
func Wrap(t tool.Tool, tp trace.TracerProvider, ids IDs) tool.Tool {
	return traced{t: t, tracer: tp.Tracer(tracerName), ids: ids}
}

var _ tool.Tool = traced{}

type traced struct {
	t      tool.Tool
	tracer trace.Tracer
	ids    IDs
}

func (t traced) Def() tool.Def { return t.t.Def() }

func (t traced) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	ctx, span := t.tracer.Start(ctx, "tool.call", trace.WithAttributes(
		attribute.String("jf.tool", t.t.Def().Name),
		attribute.String("jf.session_id", t.ids.SessionID),
		attribute.String("jf.turn_id", t.ids.TurnID),
		attribute.String("jf.call_id", in.CallID),
	))
	defer span.End()

	res, err := t.t.Call(ctx, in)
	failed := err != nil || res.IsError || ctx.Err() != nil
	span.SetAttributes(attribute.Bool("jf.is_error", failed))
	if failed {
		span.SetStatus(codes.Error, "error")
	}
	return res, err
}
