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
