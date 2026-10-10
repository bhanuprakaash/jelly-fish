# jelly-fish verification map

This directory is the maintained source for verifying jelly-fish's real behavior: the api role's HTTP surface, the worker role's health surface, migrations, and the web PWA shell. Read [`../SKILL.md`](../SKILL.md) first to launch the app, then use the matching feature file below as the recipe.

## Baseline preconditions

- `$DATABASE_URL` pointing at the port-forwarded local postgres (`localhost:5433`), taken from `jellyfish_db` as in [`../SKILL.md`](../SKILL.md) Launch.
- Local k8s infra (`postgres`, `minio`, `mailpit`) running via `make infra`; Mailpit forwarded on `:8025`.
- The cluster api redeployed and forwarded on `:8080` per [`../SKILL.md`](../SKILL.md) Launch. Always the cluster, never a locally started process. `:9090` (health) only when its forward is up.

## Driving conventions

- Every drive is a `curl` (or, for `migrate`, `kubectl logs`; for UI, a Playwright script in [`../scripts`](../scripts)) against the real cluster api — no test-only endpoints, no mocks.
- Quote the exact command, HTTP status, and response body as proof.
- Run [Doctor](../SKILL.md#doctor) before the first drive and again after any failure.

## Features

- [Liveness (`/healthz`)](./healthz.md) — the api/worker process is up.
- [Readiness (`/readyz`)](./readyz.md) — postgres is reachable and migrated.
- [Sign in](./sign-in.md) — emailed code → Login Session cookie, `/api/me`, 401 without it.
- [Admin: invites and Users](./admin-users.md) — invite a friend, first login, disable/enable, make admin, `/api/admin/*` is `404` to non-admins.
- [Hello API (`/api/hello`)](./hello-api.md) — a static JSON greeting behind sign-in.
- [Migrate role](./migrate.md) — applying goose migrations.
- [Web shell](./web-shell.md) — the signed-in Shell (Chat List + chat pane), its loading/error states, login screen and routes.
- [Chat List](./chat-list.md) — `GET /api/sessions`, desktop sidebar, phone list → chat → back, open replays from seq 0.
- [Activity Stream and badges](./activity-stream.md) — `GET /api/activity`, live running/needs-approval/failed badges, hide/show reconnect, 20-stream cap.
- [Rename and automatic Title](./rename-title.md) — `PUT /api/sessions/{id}/title`, the "⋯" Rename menu, header updates from `session.renamed`, the Worker's automatic Title.
- [S5 demo](./s5-demo.md) — the whole S5 flow on desktop and Pixel 7 emulation: live badges, Title, rename, hide/show recovery.
- [Tool calls](./tool-calls.md) — `tool.call.*` events, the parallel batch of the dev fake tools, Interrupt mid-call.
- [Memory](./memory.md) — the `memory` tool: rows + revisions, `memory.written`, taint → `pending_review`, secret refusal, no memory text in the log.
- [Connectors](./connectors.md) — Add and Remove Connector in Settings: probe, per-tool switches, slug, remove confirm, on desktop and Pixel 7 emulation.
