# jelly-fish

Single Go binary with roles `api`, `worker`, `migrate` (`cmd/jelly-fish`). Postgres via pgx, migrations via goose.

## Commands

- `make test`: run all tests
- `make chaos`: kill/freeze real worker processes (build tag `chaos`, needs `TEST_DATABASE_URL`)
- `make lint`: golangci-lint (formatters + linters)
- `make fmt`: auto-fix formatting
- `make infra` / `make db`: local k8s infra / port-forward postgres to `:5433`

Run `make lint test` before calling a change done. For changes to `internal/api`, `internal/health`, `internal/worker`, `internal/migrate`, `cmd/jelly-fish`, or `web/`, also run the `verify-jelly-fish` skill to prove the change against a live run, not just tests. Run `maintain-verification-skill` occasionally to catch drift between that skill's feature map and the app.

## Working rules

- Smallest diff that solves the task. No layer, option or guard the task doesn't need.
- Comments only for a constraint the code can't show. No internal plan ids (ticket, slice) in code, comments or commits.
- Tests check behaviour with literal expected values. Few of them.
- Commits: conventional prefix (`feat:`, `fix:`, `chore:`), one concise subject line.
- A remote agent never pushes to `main` or force-pushes. Work on a branch.
- A remote agent can't reach the local k8s cluster. When the change touches the paths above that need `verify-jelly-fish`, say in the PR or comment that it is still needed.

## Docs

- Code style: [docs/style.md](docs/style.md). Follow it, especially the Comments section.
- Decisions: `docs/adr/`. Designs: `docs/design/`.

## Gotchas

- Never run `go mod tidy`: it rewrites unrelated otel lines. Add a dependency with `go get <pkg>`; the editor's go.mod tidy warnings are expected.
- `make chaos` and DB tests need `TEST_DATABASE_URL`; a local Postgres works (`postgres:///postgres`).

## Git

- Direct push to `main` is allowed, but only with explicit approval each time — never push unasked.
