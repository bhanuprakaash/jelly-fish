---
name: verify-jelly-fish
description: "Boots jelly-fish's api/worker roles against the local k8s postgres and drives the HTTP endpoints (and proxied web shell) to prove real behavior. Use for /verify-jelly-fish, after landing a change to internal/api, internal/health, internal/worker, internal/migrate, cmd/jelly-fish, or web/, or before calling an issue done."
---

# Verify jelly-fish

jelly-fish is a single Go binary (`cmd/jelly-fish`) with three roles — `api`, `worker`, `migrate` — plus a Vite/React PWA shell in `web/` that proxies `/api` to the api role. This skill proves the running binary and its HTTP surface behave as documented, instead of trusting `make lint test` alone.

## Launch

Preconditions:

- Local k8s infra is up: `kubectl get pods -n jelly-fish` shows `postgres`, `minio`, `mailpit` all `Running`. If not, run `make infra` and wait for `postgres-0` to become ready.
- Postgres is reachable at `localhost:5433`: check with `lsof -iTCP:5433 -sTCP:LISTEN`. If nothing is listening, start it yourself with `make db &` and remember you started it (see Cleanup).
- Mailpit is reachable at `localhost:8025` (web API) and `localhost:1025` (SMTP): check with `lsof -iTCP:8025 -sTCP:LISTEN`. If nothing is listening, start it yourself with `kubectl port-forward -n jelly-fish svc/mailpit 8025:8025 1025:1025 &` and remember you started it (see Cleanup). `make serve` already forwards 8025 but not 1025.
- `$DATABASE_URL` is exported and points at `localhost:5433` with the credentials from `deploy/k8s/.env.postgres`. This is the user's own env var — read it with `echo $DATABASE_URL`, never grep or cat the `.env.postgres` file for it. If it's unset, stop and ask the user to export it.

Steps, from the repo root:

1. Apply migrations and bootstrap the Admin (idempotent, safe to rerun): `JF_ADMIN_EMAIL=admin@example.test go run ./cmd/jelly-fish migrate`. Ready signal: process exits `0`, last log line `admin bootstrap`.
2. Build a fixed binary once, then run that — never background `go run` itself. `go run` compiles and execs a *child* process; `$!` captures the short-lived wrapper PID, not the child, so killing it later leaves the real server orphaned and still listening:
   ```
   go build -o /tmp/jelly-fish-verify-bin ./cmd/jelly-fish
   JF_SMTP_URL=smtp://localhost:1025 JF_MAIL_FROM=jelly-fish@localhost JF_PUBLIC_URL=http://localhost:8080 JF_DEV_INSECURE_COOKIE=1 \
     /tmp/jelly-fish-verify-bin api > /tmp/jelly-fish-verify-api.log 2>&1 &
   echo $! > /tmp/jelly-fish-verify-api.pid
   ```
   Ready signal: `curl -sf localhost:9090/healthz` returns `ok`.
3. Optional, only when the task touches `internal/worker`: start the worker role on a separate health port (it defaults to the same `:9090` as api and will conflict):
   ```
   HEALTH_ADDR=:9091 /tmp/jelly-fish-verify-bin worker > /tmp/jelly-fish-verify-worker.log 2>&1 &
   echo $! > /tmp/jelly-fish-verify-worker.pid
   ```
   Ready signal: log line `"worker idle"` in `/tmp/jelly-fish-verify-worker.log`.
4. Optional, only when the task touches `web/`: start the PWA dev server:
   ```
   pnpm --dir web dev > /tmp/jelly-fish-verify-web.log 2>&1 &
   echo $! > /tmp/jelly-fish-verify-web.pid
   ```
   Ready signal: log line `Local:   http://localhost:5173/`.

## Doctor

Run before driving anything, and again after any failed drive:

- `curl -sf localhost:9090/healthz` → body `ok`, status `200`. Fail means the api/worker process died; check its log file.
- `curl -sf localhost:9090/readyz` → body `ok`, status `200`. Fail means postgres is unreachable or migrations haven't been applied; rerun Launch step 1.
- `kill -0 $(cat /tmp/jelly-fish-verify-api.pid)` confirms the process this run started is still the one answering — don't trust a stray process left over from a prior session.

## Drive

See [`features/`](./features/README.md) for the per-feature recipes: `healthz`, `readyz`, `sign-in`, `hello-api`, `migrate`, `web-shell`. Every `/api` route except `/api/auth/*` needs the cookie from `sign-in`.

## Evidence

For every feature, capture and quote in the report:

- The exact `curl` command, its HTTP status, and its full response body.
- For `migrate`, the command's exit code and the last log line goose prints.
- For `web-shell`, no browser automation tool is registered in this environment as of writing — proof is limited to `curl localhost:5173/api/hello` (confirms the Vite proxy reaches the api role) plus the raw HTML at `/`. Full rendered-DOM proof (the "Jelly-fish" heading and hello text) needs a browser tool (e.g. a Playwright MCP); note this gap in the report rather than claiming visual proof you didn't capture.
- Every drive hits the real api role and the real local postgres instance — no mocks, no test-only endpoints.

## Cleanup

- Kill only what this run started, by PID file, never by process name:
  ```
  for f in /tmp/jelly-fish-verify-{api,worker,web}.pid; do
    [ -f "$f" ] && kill "$(cat "$f")" 2>/dev/null; rm -f "$f"
  done
  rm -f /tmp/jelly-fish-verify-bin /tmp/jelly-fish-verify-*.log
  ```
  Confirm the ports are actually free afterward (`lsof -iTCP:8080 -sTCP:LISTEN`, `:9090`, `:9091`) — don't assume `kill` on the PID file was enough.
- If this run started the Mailpit port-forward itself, kill that `kubectl port-forward` process too.
- If this run started the postgres port-forward itself (it wasn't already listening in Launch step 2), kill that `kubectl port-forward` process too. If it was already running before this run, leave it — it's the user's persistent dev tunnel.
- Never tear down `make infra` or delete k8s resources — postgres/minio/mailpit are persistent local dev infra, not scoped to a single verification run.
- Evidence already quoted in the report survives cleanup; nothing here deletes report content, only processes.

## Helpers

None yet — every step above is a plain `go run`/`curl`/`pnpm` command run directly. Add a `scripts/` helper here only if a step above turns out to need one.
