// Package retry wraps a Provider with the quick in-process retries of
// agent-loop.md §5.3. Longer waits belong to the session, not here.
package retry

import (
	"context"
	"errors"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/provider"
)

// maxRetries is the retry budget after the first attempt.
const maxRetries = 4

// backoff is the wait before retry attempt+1 when the Provider gave no
// retry-after: 1, 2, 4 then 8 seconds.
func backoff(attempt int) time.Duration { return time.Second << attempt }

// Sleep waits d, or until ctx ends, in which case it returns ctx's error.
func Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Wrap returns a Provider that retries p's rate_limited and provider_down
// errors up to four times, waiting the Provider's retry-after or else 1, 2, 4
// then 8 seconds via sleep. Every other error returns at once. Usage from
// failed attempts is added to the final Response or provider.Error. onRetry,
// if set, runs before each retry attempt so a caller can drop the failed
// attempt's partial output.
func Wrap(p provider.Provider, sleep func(context.Context, time.Duration) error, onRetry func()) provider.Provider {
	return retrying{p: p, sleep: sleep, onRetry: onRetry}
}

type retrying struct {
	p       provider.Provider
	sleep   func(context.Context, time.Duration) error
	onRetry func()
}

func (r retrying) Name() string { return r.p.Name() }

func (r retrying) Stream(ctx context.Context, req provider.Request, onDelta func(provider.Delta)) (provider.Response, error) {
	var spent provider.Usage
	for attempt := 0; ; attempt++ {
		resp, err := r.p.Stream(ctx, req, onDelta)
		if err == nil {
			resp.Usage = sum(resp.Usage, spent)
			return resp, nil
		}
		var pe *provider.Error
		if !errors.As(err, &pe) {
			return provider.Response{}, err
		}
		spent = sum(spent, pe.Usage)
		if !pe.Kind.Retryable() || attempt == maxRetries {
			out := *pe
			out.Usage = spent
			return provider.Response{}, &out
		}
		wait := pe.RetryAfter
		if wait == 0 {
			wait = backoff(attempt)
		}
		if err := r.sleep(ctx, wait); err != nil {
			return provider.Response{}, err
		}
		if r.onRetry != nil {
			r.onRetry()
		}
	}
}

func sum(a, b provider.Usage) provider.Usage {
	return provider.Usage{
		Input:        a.Input + b.Input,
		CacheRead:    a.CacheRead + b.CacheRead,
		CacheWrite5m: a.CacheWrite5m + b.CacheWrite5m,
		CacheWrite1h: a.CacheWrite1h + b.CacheWrite1h,
		Output:       a.Output + b.Output,
		Reasoning:    a.Reasoning + b.Reasoning,
		WebSearches:  a.WebSearches + b.WebSearches,
	}
}
