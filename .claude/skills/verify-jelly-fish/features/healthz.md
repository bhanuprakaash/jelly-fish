# Liveness (`/healthz`)

Reports whether the api or worker process is up at all, independent of postgres.

## Sub-features

- `healthz-api` — the api role's `:9090/healthz`.
- `healthz-worker` — a worker pod's health port, through `kubectl port-forward`.

## How to get to it (user POV)

- Not user-facing directly; it's the endpoint the pods' liveness probes hit (`deploy/k8s/app.yaml`).

## Driving it with curl

Preconditions:

- The cluster api is deployed per [`../SKILL.md`](../SKILL.md) Launch, with `kubectl --context jelly-fish -n jelly-fish port-forward svc/api 9090:9090` running.

- **api liveness.** Run `curl -si localhost:9090/healthz`. Status `200`, body `ok`.
- **worker liveness.** Forward a worker pod: `kubectl --context jelly-fish -n jelly-fish port-forward deploy/worker 9091:9090`, then `curl -si localhost:9091/healthz`. Status `200`, body `ok`.

## Gotchas

- Pods are probed by kubelet already; `kubectl get deploy` showing them ready is corroborating evidence, not a substitute for the curl.
- `/healthz` never checks postgres. A `200` here says nothing about readiness; use `readyz` for that.
