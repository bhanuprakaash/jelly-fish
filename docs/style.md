# Go style

Formatting, imports, and anything `make lint` enforces is not repeated here.

## Errors

- Wrap with context: `fmt.Errorf("load config: %w", err)`. No "failed to" prefix.
- Handle an error once: log it or return it, never both.
- Sentinel errors are `errFoo` / `ErrFoo`; error types are `FooError`.
- Compare with `errors.Is` / `errors.As`, never `==` or type assertion.
- No `panic` outside `main` and truly impossible states. `os.Exit` / `log.Fatal` only in `main`.

## Design

- Accept interfaces, return concrete types. Define interfaces where they are consumed.
- Verify interface compliance at compile time: `var _ Store = (*pgStore)(nil)`.
- No mutable package globals, no `init()`. Wire dependencies explicitly from `main`.
- Copy slices and maps received or returned across API boundaries when the caller could mutate them.
- Start enums at `iota + 1` unless the zero value is a meaningful default.
- Use functional options only when a constructor has 3+ optional params.
- Zero-value structs should be usable where practical; avoid pointer-to-interface.

## Concurrency

- Never fire-and-forget a goroutine: every one has a stop signal (ctx) and a way to wait for it (`sync.WaitGroup`, `errgroup`).
- `context.Context` is the first param, named `ctx`. Never store it in a struct.
- Channels are unbuffered or size 1 unless a larger size is justified.
- Use `defer` to release locks, files, rows, and response bodies.

## Control flow

- Happy path unindented: return early on errors and edge cases.
- No `else` after a `return`.
- Keep variable scope tight (`if err := f(); err != nil`).

## Logging

- `log/slog` only, injected as `*slog.Logger`. Structured keys, lower-case message: `logger.Info("job claimed", "job_id", id)`.
- Error key is `"error"`.

## Tests

- Table-driven tests with `t.Run` for multiple cases.
- Test behaviour through the public API; don't export things just for tests.
- Use `t.Helper()`, `t.Cleanup()`, `t.Context()`.

## Comments

- Packages and exported identifiers get a doc comment: a full sentence starting with the name (`// NewServer builds ...`).
- Comment why, not what. Short, timeless, describes the code as it is now.
- No chat/issue history, "fixed bug where...", "now"/"new", narration, or commented-out code. That belongs in the commit/PR.
- Link an ADR instead of re-explaining a decision. `TODO` needs an owner or issue.
