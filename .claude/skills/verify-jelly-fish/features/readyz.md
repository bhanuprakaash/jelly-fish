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

- The cluster api is deployed per [`../SKILL.md`](../SKILL.md) Launch, with `kubectl --context jelly-fish -n jelly-fish port-forward svc/api 9090:9090` running.

- **Ready.** The rollout already ran the migrate init container. Run `curl -si localhost:9090/readyz`. Status `200`, body `ok`.
- **Unmigrated** and **DB down.** Not drivable on the cluster without breaking the shared dev database. Report them as skipped; the unit tests in `internal/pg` / `internal/health` cover them.

## Gotchas

- The unmigrated and db-down drives are destructive to shared state if run against the real dev postgres; report them as skipped — don't fake the result.
- `readyz` pings postgres on every request; don't loop-poll it under load, it isn't a spam target.
