// Package fake implements Tools for local and deployed dev use. It is wired
// in only under the dev build tag.
package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/bhanuprakaash/jelly-fish/internal/tool"
)

// Tools returns the dev tools. A call to slow_side_effect appends its
// idempotency key to the file at sideEffectLog, so a test can count how many
// times the side effect happened.
func Tools(sideEffectLog string) []tool.Tool {
	return []tool.Tool{sleep{}, fetch{}, slowSideEffect{log: sideEffectLog}}
}

const schema = `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`

func input(in tool.CallInput) string {
	var a struct {
		Input string `json:"input"`
	}
	_ = json.Unmarshal(in.Args, &a)
	return a.Input
}

// wait blocks for the duration in s, or until ctx ends.
func wait(ctx context.Context, s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	select {
	case <-time.After(d):
		return d, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// sleep waits for the duration in its input. It is parallel-safe.
type sleep struct{}

func (sleep) Def() tool.Def {
	return tool.Def{Name: "sleep", Description: "Wait for a duration such as 2s.", Schema: json.RawMessage(schema), ParallelSafe: true}
}

func (sleep) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	d, err := wait(ctx, input(in))
	if err != nil {
		return tool.TextResult(err.Error(), true), nil
	}
	return tool.TextResult("slept "+d.String(), false), nil
}

// fetch stands in for a web fetch: its output is untrusted.
type fetch struct{}

func (fetch) Def() tool.Def {
	return tool.Def{Name: "fetch", Description: "Fetch a page.", Schema: json.RawMessage(schema), ParallelSafe: true, Untrusted: true}
}

func (fetch) Call(_ context.Context, in tool.CallInput) (tool.Result, error) {
	return tool.TextResult("fetched "+input(in), false), nil
}

// slowSideEffect records that it ran, then waits.
type slowSideEffect struct{ log string }

func (slowSideEffect) Def() tool.Def {
	return tool.Def{Name: "slow_side_effect", Description: "Record a side effect, then wait for a duration such as 2s.", Schema: json.RawMessage(schema)}
}

func (s slowSideEffect) Call(ctx context.Context, in tool.CallInput) (tool.Result, error) {
	if s.log != "" {
		f, err := os.OpenFile(s.log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return tool.Result{}, fmt.Errorf("open side effect log: %w", err)
		}
		_, err = fmt.Fprintln(f, in.IdempotencyKey)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return tool.Result{}, fmt.Errorf("write side effect log: %w", err)
		}
	}
	d, err := wait(ctx, input(in))
	if err != nil {
		return tool.TextResult(err.Error(), true), nil
	}
	return tool.TextResult("done after "+d.String(), false), nil
}
