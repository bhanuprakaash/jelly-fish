package retry_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
	"github.com/bhanuprakaash/jelly-fish/internal/provider/retry"
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
	return provider.Response{StopReason: provider.StopReasonEndTurn, Usage: provider.Usage{Input: 1, Output: 2}}, nil
}

func fail(kind provider.ErrorKind, wait time.Duration, in int64) error {
	return &provider.Error{Kind: kind, RetryAfter: wait, RequestID: "req_x", Usage: provider.Usage{Input: in}}
}

type sleeps struct{ waits []time.Duration }

func (s *sleeps) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func TestOverloadedFourTimesThenSuccess(t *testing.T) {
	p := &scripted{results: []error{
		fail(provider.KindProviderDown, 3*time.Second, 10),
		fail(provider.KindProviderDown, 5*time.Second, 10),
		fail(provider.KindProviderDown, 7*time.Second, 10),
		fail(provider.KindProviderDown, 11*time.Second, 10),
		nil,
	}}
	var s sleeps
	resets := 0
	resp, err := retry.Wrap(p, s.sleep, func() { resets++ }).Stream(t.Context(), provider.Request{}, func(provider.Delta) {})
	if err != nil {
		t.Fatal(err)
	}
	want := []time.Duration{3 * time.Second, 5 * time.Second, 7 * time.Second, 11 * time.Second}
	if p.calls != 5 || len(s.waits) != 4 || resets != 4 {
		t.Fatalf("calls=%d sleeps=%v resets=%d, want 5 calls, 4 sleeps, 4 resets", p.calls, s.waits, resets)
	}
	for i := range want {
		if s.waits[i] != want[i] {
			t.Errorf("wait %d = %v, want the scripted retry-after %v", i, s.waits[i], want[i])
		}
	}
	if resp.Usage != (provider.Usage{Input: 41, Output: 2}) {
		t.Fatalf("usage = %+v, want the failed attempts summed in", resp.Usage)
	}
}

func TestBacksOffWithoutRetryAfter(t *testing.T) {
	p := &scripted{results: []error{fail(provider.KindRateLimited, 0, 0)}}
	var s sleeps
	_, err := retry.Wrap(p, s.sleep, nil).Stream(t.Context(), provider.Request{}, func(provider.Delta) {})
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second}
	if len(s.waits) != len(want) {
		t.Fatalf("sleeps = %v, want %v", s.waits, want)
	}
	for i := range want {
		if s.waits[i] != want[i] {
			t.Errorf("wait %d = %v, want %v", i, s.waits[i], want[i])
		}
	}
	var pe *provider.Error
	if !errors.As(err, &pe) || pe.Kind != provider.KindRateLimited {
		t.Fatalf("err = %v, want the last provider error", err)
	}
}

func TestAllAttemptsFailKeepsUsageAndRequestID(t *testing.T) {
	p := &scripted{results: []error{fail(provider.KindProviderDown, 0, 10)}}
	var s sleeps
	_, err := retry.Wrap(p, s.sleep, nil).Stream(t.Context(), provider.Request{}, func(provider.Delta) {})
	var pe *provider.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v", err)
	}
	if p.calls != 5 || pe.Usage.Input != 50 || pe.RequestID != "req_x" {
		t.Fatalf("calls=%d usage=%+v request=%q, want 5 calls, 50 input tokens, req_x", p.calls, pe.Usage, pe.RequestID)
	}
}

func TestStopClassesAndLongWaitAreNotRetried(t *testing.T) {
	for _, kind := range []provider.ErrorKind{
		provider.KindKeyInvalid, provider.KindBilling, provider.KindModelUnavailable,
		provider.KindTooLarge, provider.KindBug, provider.KindLongWait,
	} {
		t.Run(string(kind), func(t *testing.T) {
			p := &scripted{results: []error{fail(kind, 90*time.Second, 0)}}
			var s sleeps
			_, err := retry.Wrap(p, s.sleep, nil).Stream(t.Context(), provider.Request{}, func(provider.Delta) {})
			var pe *provider.Error
			if !errors.As(err, &pe) || pe.Kind != kind {
				t.Fatalf("err = %v, want %s", err, kind)
			}
			if p.calls != 1 || len(s.waits) != 0 {
				t.Fatalf("calls=%d sleeps=%v, want one call and no sleep", p.calls, s.waits)
			}
		})
	}
}

func TestOtherErrorsPassThrough(t *testing.T) {
	boom := errors.New("boom")
	p := &scripted{results: []error{boom}}
	_, err := retry.Wrap(p, new(sleeps).sleep, nil).Stream(t.Context(), provider.Request{}, func(provider.Delta) {})
	if !errors.Is(err, boom) || p.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, p.calls)
	}
}

func TestCancelStopsTheWait(t *testing.T) {
	p := &scripted{results: []error{fail(provider.KindProviderDown, time.Hour, 0)}}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := retry.Wrap(p, retry.Sleep, nil).Stream(ctx, provider.Request{}, func(provider.Delta) {})
	if !errors.Is(err, context.Canceled) || p.calls != 1 {
		t.Fatalf("err=%v calls=%d, want the ctx error after one call", err, p.calls)
	}
}

func TestNamePassesThrough(t *testing.T) {
	if got := retry.Wrap(&scripted{}, nil, nil).Name(); got != "scripted" {
		t.Fatalf("Name = %q", got)
	}
}
