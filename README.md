# jelly-fish

## Prerequisites

- Local k8s (Docker Desktop, OrbStack, etc.) with `kubectl` pointed at it
- [Tailscale](https://tailscale.com/) installed and signed in on the Mac and on your phone, same tailnet

## Run

```
cp deploy/k8s/.env.postgres.example deploy/k8s/.env.postgres
cp deploy/k8s/.env.minio.example deploy/k8s/.env.minio
make infra deploy serve
```

`make serve` port-forwards `svc/api` and `svc/postgres`, then serves the API over HTTPS on the tailnet via `tailscale serve`.

On your phone, open `https://<mac>.<tailnet>.ts.net` and install to the home screen.

## Agent sandbox

Coding agents run in a [Docker Sandbox](https://docs.docker.com/ai/sandboxes/) with their own k3d cluster and `localhost:5001` registry, so `make infra deploy db test lint` work inside unchanged. Kit: `.sbx/jelly-fish/`.

```
sbx policy init balanced   # once per machine
make sandbox               # clone-mode sandbox + your ~/.claude/CLAUDE.md
sbx run jelly-fish         # attach; /login on first use
```

The sandbox works on its own clone. Fetch its commits with `git fetch sandbox-jelly-fish` and push from the host. Phone and Tailscale checks stay on the host.
