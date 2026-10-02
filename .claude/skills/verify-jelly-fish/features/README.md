# jelly-fish verification map

This directory is the maintained source for verifying jelly-fish's real behavior: the api role's HTTP surface, the worker role's health surface, migrations, and the web PWA shell. Read [`../SKILL.md`](../SKILL.md) first to launch the app, then use the matching feature file below as the recipe.

## Baseline preconditions

- `$DATABASE_URL` pointing at the port-forwarded local postgres (`localhost:5433`), taken from `jellyfish_db` as in [`../SKILL.md`](../SKILL.md) Launch.
- Local k8s infra (`postgres`, `minio`, `mailpit`) running via `make infra`; Mailpit forwarded on `:8025` and `:1025`.
- The api role started per [`../SKILL.md`](../SKILL.md) Launch, answering on `:8080` (api) and `:9090` (health).

## Driving conventions

- Every drive is a `curl` (or, for `migrate`, a direct `go run`) against the real running process — no test-only endpoints, no mocks.
- Quote the exact command, HTTP status, and response body as proof.
- Run [Doctor](../SKILL.md#doctor) before the first drive and again after any failure.

## Features

- [Liveness (`/healthz`)](./healthz.md) — the api/worker process is up.
- [Readiness (`/readyz`)](./readyz.md) — postgres is reachable and migrated.
- [Sign in](./sign-in.md) — emailed code → Login Session cookie, `/api/me`, 401 without it.
- [Hello API (`/api/hello`)](./hello-api.md) — a static JSON greeting behind sign-in.
- [Migrate role](./migrate.md) — applying goose migrations.
- [Web shell](./web-shell.md) — the PWA rendering the hello message.
