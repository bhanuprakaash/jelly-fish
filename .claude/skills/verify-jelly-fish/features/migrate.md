# Migrate role

Applies goose migrations from `migrations/` against `$DATABASE_URL`. Currently one migration: `00001_enable_pgcrypto.sql`.

## Sub-features

- `migrate-fresh` — applying to a database with no goose table yet.
- `migrate-idempotent` — rerunning against an already-migrated database is a no-op.

## How to get to it (user POV)

- Not interactive; it's the `migrate` role of the binary: `go run ./cmd/jelly-fish migrate` (or the built binary in a real deploy).

## Driving it directly

There's no HTTP surface for this feature — it's a CLI/process action, proven through its exit code and through `readyz`.

Preconditions:

- `$DATABASE_URL` exported.

- **Apply.** Run `go run ./cmd/jelly-fish migrate`. Exit code `0`.
- **Confirm applied.** Start the api role afterward (Launch steps 1 then 2, in that order) and run `curl -si localhost:9090/readyz`. Status `200`, body `ok` — this is the actual proof migrations landed, since `readyz` checks for the goose version table.
- **Idempotent rerun.** Run `go run ./cmd/jelly-fish migrate` a second time. Exit code `0` again, with goose reporting the database already at the latest version, no new migration applied.

## Gotchas

- jelly-fish exposes no standalone "list applied migrations" command; `readyz`'s existence check is the only external proof available today.
- Running `migrate` against a `DATABASE_URL` that doesn't match what the api role was started with proves nothing about that api instance's readiness.
