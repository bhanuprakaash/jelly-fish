# Migrate role

Applies goose migrations from `migrations/` against `$DATABASE_URL`. Currently one migration: `00001_enable_pgcrypto.sql`.

## Sub-features

- `migrate-fresh` — applying to a database with no goose table yet.
- `migrate-idempotent` — rerunning against an already-migrated database is a no-op.

## How to get to it (user POV)

- Not interactive; it's the `migrate` role of the binary, run as the api pod's `migrate` init container on every rollout.

## Driving it directly

There's no HTTP surface for this feature — it's a CLI/process action, proven through its exit code and through `readyz`.

Preconditions:

- The cluster api redeployed per [`../SKILL.md`](../SKILL.md) Launch (the rollout runs migrate).

- **Apply.** `kubectl --context jelly-fish -n jelly-fish logs deploy/api -c migrate`. The init container exited `0`; goose lists any migration it applied, then `admin bootstrap`.
- **Confirm applied.** The api pod went Ready (`rollout status`), and with the `:9090` forward up, `curl -si localhost:9090/readyz` returns `200`, body `ok`. This is the actual proof migrations landed, since `readyz` checks for the goose version table.
- **Idempotent rerun.** `kubectl --context jelly-fish -n jelly-fish rollout restart deploy/api`, wait for it, and read the init log again: `no pending migrations`, exit `0`.

## Gotchas

- jelly-fish exposes no standalone "list applied migrations" command; `readyz`'s existence check is the only external proof available today.
