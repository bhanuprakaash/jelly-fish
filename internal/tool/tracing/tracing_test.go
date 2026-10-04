package tracing_test

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/bhanuprakaash/jelly-fish/internal/tool"
	"github.com/bhanuprakaash/jelly-fish/internal/tool/tracing"
)

type stub struct {
	res tool.Result
	err error
}

func (stub) Def() tool.Def { return tool.Def{Name: "stub"} }

func (s stub) Call(context.Context, tool.CallInput) (tool.Result, error) { return s.res, s.err }

func TestCallSpan(t *testing.T) {
	tests := []struct {
		name       string
		tool       stub
		wantErr    bool
		wantStatus codes.Code
	}{
		{"ok", stub{res: tool.TextResult("ok", false)}, false, codes.Unset},
		{"error result", stub{res: tool.TextResult("bad", true)}, true, codes.Error},
		{"error", stub{err: errors.New("boom")}, true, codes.Error},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := tracetest.NewSpanRecorder()
			tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
			t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

			wrapped := tracing.Wrap(tt.tool, tp, tracing.IDs{SessionID: "sess-1", TurnID: "turn-1"})
			_, _ = wrapped.Call(context.Background(), tool.CallInput{CallID: "c1"})

			spans := rec.Ended()
			if len(spans) != 1 || spans[0].Name() != "tool.call" {
				t.Fatalf("spans = %v, want one tool.call", spans)
			}
			got := map[string]string{}
			for _, kv := range spans[0].Attributes() {
				got[string(kv.Key)] = kv.Value.Emit()
			}
			wantErr := "false"
			if tt.wantErr {
				wantErr = "true"
			}
			want := map[string]string{"jf.tool": "stub", "jf.session_id": "sess-1", "jf.turn_id": "turn-1", "jf.call_id": "c1", "jf.is_error": wantErr}
			for k, v := range want {
				if got[k] != v {
					t.Errorf("%s = %q, want %q", k, got[k], v)
				}
			}
			if s := spans[0].Status().Code; s != tt.wantStatus {
				t.Errorf("status = %v, want %v", s, tt.wantStatus)
			}
		})
	}
}
