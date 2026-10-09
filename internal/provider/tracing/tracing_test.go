package tracing_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/bhanuprakaash/jelly-fish/internal/msg"
	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/retry"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/tracing"
)

type scripted struct {
	results []error
	calls   int
}

func (s *scripted) Name() string { return "scripted" }

func (s *scripted) Stream(_ context.Context, _ provider.Request, _ func(provider.Delta)) (provider.Response, error) {
	err := s.results[min(s.calls, len(s.results)-1)]
	s.calls++
	if err != nil {
		return provider.Response{}, err
	}
	return provider.Response{
		StopReason: provider.StopReasonEndTurn,
		RequestID:  "req_ok",
		Usage:      provider.Usage{Input: 100, CacheRead: 20, CacheWrite5m: 3, CacheWrite1h: 4, Output: 50, Reasoning: 7},
	}, nil
}

func recorder(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	return tp, rec
}

func attrs(s sdktrace.ReadOnlySpan) map[string]attribute.Value {
	m := map[string]attribute.Value{}
	for _, kv := range s.Attributes() {
		m[string(kv.Key)] = kv.Value
	}
	return m
}

func ids() tracing.IDs {
	return tracing.IDs{SessionID: "sess-1", TurnID: "turn-1"}
}

func TestSuccessSpanCarriesCallAttributes(t *testing.T) {
	tp, rec := recorder(t)
	p := tracing.Wrap(&scripted{results: []error{nil}}, tp, ids())

	if _, err := p.Stream(t.Context(), provider.Request{Model: "claude-haiku"}, func(provider.Delta) {}); err != nil {
		t.Fatal(err)
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	got := attrs(spans[0])
	want := map[string]any{
		"gen_ai.provider.name":               "scripted",
		"gen_ai.request.model":               "claude-haiku",
		"jf.session_id":                      "sess-1",
		"jf.turn_id":                         "turn-1",
		"jf.request_id":                      "req_ok",
		"gen_ai.usage.input_tokens":          int64(100),
		"gen_ai.usage.cache_read_tokens":     int64(20),
		"gen_ai.usage.cache_write_5m_tokens": int64(3),
		"gen_ai.usage.cache_write_1h_tokens": int64(4),
		"gen_ai.usage.output_tokens":         int64(50),
		"gen_ai.usage.reasoning_tokens":      int64(7),
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g.AsInterface() != v {
			t.Errorf("attribute %s = %v, want %v", k, g.AsInterface(), v)
		}
	}
	if r := got["gen_ai.response.finish_reasons"].AsStringSlice(); len(r) != 1 || r[0] != "end_turn" {
		t.Errorf("finish reasons = %v, want [end_turn]", r)
	}
	if _, ok := got["jf.error_class"]; ok {
		t.Error("a successful call has an error class")
	}
	if spans[0].Status().Code != 0 {
		t.Errorf("status = %v, want unset", spans[0].Status())
	}
}

func TestEveryRetryAttemptGetsItsOwnSpan(t *testing.T) {
	tp, rec := recorder(t)
	down := func(id string, in int64) error {
		return &provider.Error{Kind: provider.KindProviderDown, RequestID: id, HTTPStatus: 503, Usage: provider.Usage{Input: in}}
	}
	inner := &scripted{results: []error{down("req_1", 10), down("req_2", 11), nil}}
	noSleep := func(context.Context, time.Duration) error { return nil }
	p := retry.Wrap(tracing.Wrap(inner, tp, ids()), noSleep, nil)

	if _, err := p.Stream(t.Context(), provider.Request{Model: "m"}, func(provider.Delta) {}); err != nil {
		t.Fatal(err)
	}

	spans := rec.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3 (one per attempt)", len(spans))
	}
	for i, wantID := range []string{"req_1", "req_2", "req_ok"} {
		got := attrs(spans[i])
		if got["jf.request_id"].AsString() != wantID {
			t.Errorf("span %d request id = %q, want %q", i, got["jf.request_id"].AsString(), wantID)
		}
	}
	failed := attrs(spans[0])
	if failed["jf.error_class"].AsString() != "provider_down" || failed["gen_ai.usage.input_tokens"].AsInt64() != 10 {
		t.Errorf("failed attempt attributes = %v, want class provider_down and its own usage", failed)
	}
	if failed["http.response.status_code"].AsInt64() != 503 {
		t.Errorf("failed attempt status code = %v, want 503", failed["http.response.status_code"])
	}
	if _, ok := attrs(spans[2])["http.response.status_code"]; ok {
		t.Error("a successful attempt has a status code")
	}
	if spans[0].Status().Code != codes.Error || spans[2].Status().Code == codes.Error {
		t.Errorf("statuses = %v, %v, want error then ok", spans[0].Status(), spans[2].Status())
	}
}

func TestSpansNeverHoldKeyOrContent(t *testing.T) {
	tp, rec := recorder(t)
	const canary = "sk-ant-CANARY-secret"
	leaky := &scripted{results: []error{&provider.Error{
		Kind: provider.KindBug, RequestID: "req_x", Err: fmt.Errorf("echoed %s and hello world", canary),
	}}}
	p := tracing.Wrap(leaky, tp, ids())
	req := provider.Request{
		Model:    "m",
		System:   []string{"system " + canary},
		Messages: []msg.Message{msg.UserText("hello world " + canary)},
	}

	_, _ = p.Stream(t.Context(), req, func(provider.Delta) {})

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	dump := fmt.Sprint(spans[0].Attributes(), spans[0].Status(), spans[0].Events())
	for _, secret := range []string{canary, "hello world"} {
		if strings.Contains(dump, secret) {
			t.Errorf("span holds %q: %s", secret, dump)
		}
	}
}

func TestEmptyRequestIDAndUsageAreOmitted(t *testing.T) {
	tp, rec := recorder(t)
	p := tracing.Wrap(&scripted{results: []error{&provider.Error{Kind: provider.KindKeyInvalid}}}, tp, ids())

	_, _ = p.Stream(t.Context(), provider.Request{Model: "m"}, func(provider.Delta) {})

	got := attrs(rec.Ended()[0])
	for _, k := range []string{"jf.request_id", "gen_ai.usage.input_tokens"} {
		if _, ok := got[k]; ok {
			t.Errorf("attribute %s is set on a failure that has none", k)
		}
	}
	if got["jf.error_class"].AsString() != "key_invalid" {
		t.Errorf("error class = %q, want key_invalid", got["jf.error_class"].AsString())
	}
}
