# Readiness (`/readyz`)

Reports whether postgres is reachable and migrated, via `internal/pg.ReadinessChecker`.

## Sub-features

- `readyz-ok` — postgres reachable and the goose version table exists.
- `readyz-db-down` — postgres unreachable.
- `readyz-unmigrated` — postgres reachable but migrations never ran.

## How to get to it (user POV)

- Not user-facing; it's what a readiness probe would hit before routing traffic to the pod.

## Driving it with curl

Preconditions:

- The api role is running per [`../SKILL.md`](../SKILL.md) Launch step 2.
- `$DATABASE_URL` points at the same postgres the api role was started against.

- **Ready.** With migrations already applied (Launch step 1 ran first). Run `curl -si localhost:9090/readyz`. Status `200`, body `ok`.
- **Unmigrated.** Only worth proving against a throwaway database, never the shared dev postgres: point `DATABASE_URL` at a fresh database with no goose table, start the api role against it, run `curl -si localhost:9090/readyz`. Status `503`, body `not ready`. Skip this drive when there's no disposable database handy.
- **DB down.** Only in a disposable cluster, never the shared dev cluster: stop the postgres port-forward you started yourself, or scale `postgres-0` to `0`. Run `curl -si localhost:9090/readyz`. Status `503`, body `not ready`.

## Gotchas

- The unmigrated and db-down drives are destructive to shared state if run against the real dev postgres; only prove them with a disposable instance, and report them as skipped otherwise — don't fake the result.
- `readyz` pings postgres on every request; don't loop-poll it under load, it isn't a spam target.
