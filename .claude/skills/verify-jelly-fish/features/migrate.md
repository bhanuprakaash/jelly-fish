# Migrate role

Applies goose migrations from `migrations/` against `$DATABASE_URL`. Five migrations so far, `00001_enable_pgcrypto.sql` … `00005_chat_list.sql`; it then bootstraps the `JF_ADMIN_EMAIL` Admin.

## Sub-features

- `migrate-fresh` — applying to a database with no goose table yet.
- `migrate-idempotent` — rerunning against an already-migrated database is a no-op.

## How to get to it (user POV)

- Not interactive; it's the `migrate` role of the binary, run as the api pod's `migrate` init container on every rollout.

## Driving it directly

There's no HTTP surface for this feature — it's a CLI/process action, proven through its exit code and through `readyz`.

Preconditions:

- The cluster api redeployed per [`../SKILL.md`](../SKILL.md) Launch (the rollout runs migrate).

- **Apply.** `kubectl --context jelly-fish -n jelly-fish logs deploy/api -c migrate`. The init container exited `0`; the log is JSON slog lines: `applied migration` with `version` for each one applied (or `no pending migrations`), then `admin bootstrap` with `created`.
- **Confirm applied.** The api pod went Ready (`rollout status`), and with the `:9090` forward up, `curl -si localhost:9090/readyz` returns `200`, body `ok`. This is the actual proof migrations landed, since `readyz` checks for the goose version table.
- **Version.** `psql "$DATABASE_URL" -Atc "SELECT max(version_id) FROM goose_db_version"` returns the newest file's number (`5`).
- **Idempotent rerun.** `kubectl --context jelly-fish -n jelly-fish rollout restart deploy/api`, wait for it, and read the init log again: `no pending migrations`, exit `0`.

## Gotchas

- jelly-fish exposes no standalone "list applied migrations" command. `readyz` only checks that the goose table exists; read `goose_db_version` through `DATABASE_URL` for the version.
