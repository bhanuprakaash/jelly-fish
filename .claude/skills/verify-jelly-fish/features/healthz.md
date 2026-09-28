# Liveness (`/healthz`)

Reports whether the api or worker process is up at all, independent of postgres.

## Sub-features

- `healthz-api` — the api role's `:9090/healthz`.
- `healthz-worker` — the worker role's health port, when started with `HEALTH_ADDR=:9091`.

## How to get to it (user POV)

- Not user-facing directly; it's the endpoint a deployment's liveness probe would hit. jelly-fish has no k8s manifest of its own yet (only `deploy/k8s` for postgres/minio/mailpit).

## Driving it with curl

Preconditions:

- The api role is running per [`../SKILL.md`](../SKILL.md) Launch step 2.

- **api liveness.** Run `curl -si localhost:9090/healthz`. Status `200`, body `ok`.
- **worker liveness.** Only if the worker was started (Launch step 3). Run `curl -si localhost:9091/healthz`. Status `200`, body `ok`.

## Gotchas

- api and worker both default `HEALTH_ADDR` to `:9090`. Running both without overriding one makes the second `go run` fail to bind and exit immediately — check its log, don't assume it's up.
- `/healthz` never checks postgres. A `200` here says nothing about readiness; use `readyz` for that.
