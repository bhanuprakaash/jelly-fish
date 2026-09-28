# jelly-fish

Single Go binary with roles `api`, `worker`, `migrate` (`cmd/jelly-fish`). Postgres via pgx, migrations via goose.

## Commands

- `make test`: run all tests
- `make lint`: golangci-lint (formatters + linters)
- `make fmt`: auto-fix formatting
- `make infra` / `make db`: local k8s infra / port-forward postgres to `:5433`

Run `make lint test` before calling a change done.

## Docs

- Code style: [docs/style.md](docs/style.md). Follow it, especially the Comments section.
- Decisions: `docs/adr/`. Designs: `docs/design/`.

## Git

- Direct push to `main` is allowed, but only with explicit approval each time — never push unasked.
