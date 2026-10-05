---
name: verify-jelly-fish
description: "Redeploys the working tree to the local k8s cluster (context jelly-fish) and drives the HTTP endpoints there to prove real behavior. Never starts a local api/worker. Use for /verify-jelly-fish, after landing a change to internal/api, internal/health, internal/worker, internal/migrate, cmd/jelly-fish, or web/, or before calling an issue done."
---

# Verify jelly-fish

jelly-fish is a single Go binary (`cmd/jelly-fish`) with three roles — `api`, `worker`, `migrate` — plus a Vite/React PWA shell in `web/`, embedded in the image. This skill proves the running binary and its HTTP surface behave as documented, instead of trusting `make lint test` alone.

## Target: the cluster, only

Always verify against the api running in the local k8s cluster (kubectl context `jelly-fish`, namespace `jelly-fish`), reached through its port-forward on `:8080`. "The cluster" is the local environment; there is no other.

- **Never start a local api, worker or migrate** (`go run`, a built binary, `pnpm dev`) for verification. The cluster api already has Postgres, Mailpit and the Admin wired.
- Pass `--context jelly-fish` on every kubectl call; the current context may be a remote cluster.
- The cluster api runs `JF_PUBLIC_URL=http://localhost:8080` and no `JF_DEV_INSECURE_COOKIE`, so the cookie is `__Host-jf_login` (`Secure`). curl still sends it to `localhost` over http.
- The Admin is the `JF_ADMIN_EMAIL` from `deploy/k8s/.env.app`, bootstrapped by the migrate init container. Sign in as that email; to read which one it is, `SELECT email FROM users WHERE is_admin` through `jellyfish_db`.
- Emails land in the cluster's Mailpit, read at `localhost:8025` (`curl -s localhost:8025/api/v1/messages`, newest first).

## Launch

1. Check the forwards: `lsof -iTCP -sTCP:LISTEN -P | grep -E ':(5433|8025|8080)\b'`. Start a missing one in the background and remember you did (see Cleanup):
   - `kubectl --context jelly-fish -n jelly-fish port-forward svc/postgres 5433:5432` (or `make db`)
   - `kubectl --context jelly-fish -n jelly-fish port-forward svc/mailpit 8025:8025`
   - `kubectl --context jelly-fish -n jelly-fish port-forward svc/api 8080:8080`
2. Redeploy the working tree so the cluster runs what you are verifying (skip only if nothing changed since the last deploy):
   ```
   docker build --build-arg TAGS=dev -t localhost:5001/jelly-fish:latest . \
     && docker push localhost:5001/jelly-fish:latest \
     && kubectl --context jelly-fish -n jelly-fish rollout restart deploy/api deploy/worker \
     && kubectl --context jelly-fish -n jelly-fish rollout status deploy/api --timeout=120s \
     && kubectl --context jelly-fish -n jelly-fish rollout status deploy/worker --timeout=120s
   ```
   The migrate init container runs with the api pod, so migrations and the Admin bootstrap are applied by the rollout.
3. A rollout kills the pod behind an existing `:8080` forward. Kill that forward by its PID (`lsof -iTCP:8080 -sTCP:LISTEN`) and restart it in the background (command above). Confirm the new build answers with a curl only it passes, such as a new route's status.
4. Health ports are not forwarded by default. For `healthz`/`readyz`, forward `svc/api 9090:9090` in the background.

Ready signal: `curl -s -o /dev/null -w '%{http_code}' localhost:8080/api/me` returns `401` (the api answers and the gate is up).

## Doctor

Run before driving anything, and again after any failed drive:

- `kubectl --context jelly-fish -n jelly-fish get deploy api worker` shows `1/1` and `2/2` ready.
- `kubectl --context jelly-fish -n jelly-fish logs deploy/api --tail=20` shows no errors.
- With the `:9090` forward up: `curl -sf localhost:9090/healthz` and `/readyz` return `ok`.

## Drive

See [`features/`](./features/README.md) for the per-feature recipes: `healthz`, `readyz`, `sign-in`, `admin-users`, `hello-api`, `migrate`, `web-shell`, `chat-list`, `activity-stream`, `rename-title`, `s5-demo`, `tool-calls`, `memory`. Every `/api` route except `/api/auth/*` needs the cookie from `sign-in`.

UI flows are driven with the Playwright **library** in [`scripts/`](./scripts) (never the Playwright MCP). Install once: `cd .claude/skills/verify-jelly-fish/scripts && npm install`. `playwright` is pinned to `1.62.1` because its Chromium build (`chromium-1234`) is the one cached in `~/Library/Caches/ms-playwright`. If you bump the pin, run `npx playwright install chromium`. Browsers: desktop Chromium (1280×800) and Playwright's `Pixel 7` emulation, which is desktop Chromium with a phone viewport, UA, touch and `isMobile`. It is not a real Android browser, so a real-device or installed-PWA check stays with the user.

## Evidence

For every feature, capture and quote in the report:

- The exact `curl` command, its HTTP status, and its full response body.
- For `migrate`, the api pod's `migrate` init-container log (`kubectl --context jelly-fish -n jelly-fish logs deploy/api -c migrate`) and the rollout status.
- For UI flows, every `scripts/*.mjs` drive records into `$JF_EVIDENCE/<subdir>/` (default `~/Downloads/jelly-fish-verify/`): `<flow>.webm` video, `<flow>-trace.zip` (open with `npx playwright show-trace`), numbered PNGs, and one `PASS`/`FAIL` line per check on stdout. Quote the PASS/FAIL lines and the evidence paths, and look at the key screenshots before claiming a visual result. Never commit evidence.
- Pair a UI check with its side effect where one exists: the API response (`page.request.get`) or the DB row (`psql` via `DATABASE_URL`).
- Every drive hits the real cluster api and the real cluster postgres. No mocks, no test-only endpoints. The one exception is `page.route` fault injection inside the browser, which `web-shell` uses for its loading and error states. It changes nothing on the cluster, and the report must label it as fault injection.

## Cleanup

- Leave the deployment and the persistent forwards the user already had running (`:5433`, `:8025`, `:8080`).
- Kill only a forward this run started, by its PID, never by process name; confirm the port is free afterward.
- Never tear down `make infra` or delete k8s resources.
- Test Users and chat sessions made by a drive stay in the dev DB; list them in the report so the user can clean them up.
- Evidence already quoted in the report survives cleanup.

## Helpers

Deploy, Doctor and curl drives are plain `docker`/`kubectl`/`curl` commands. Browser drives use [`scripts/`](./scripts):

- `node signin.mjs` signs the Admin in once and prints `/api/me`. It reuses `$TMPDIR/jf-verify-auth.json` (Playwright `storageState`) while `/api/me` still accepts it. Login codes are capped at 3 per email per 15 min, so don't delete that file between drives.
- `lib.mjs` exports the following:
  - `open({dir, name, device: 'desktop'|'pixel7'})` returns a signed-in, video-recorded, traced page, with `shot(label)` and `close()`.
  - `streams(page)` returns every `EventSource` the page opened (url, readyState).
  - `setVisibility(page, 'hidden'|'visible')` fires `visibilitychange`.
  - `psql(sql)` runs a query.
  - `adminEmail()` returns the Admin's email.
  - Drive helpers: `checker(prefix)` gives `{check, exit}` (one PASS/FAIL line per check, non-zero exit on any FAIL); `nav`/`row`/`badge`/`header` return Chat List handles; `seen`/`gone`/`until` wait on a locator or a condition; `fakeTitle(first)` gives the Fake Provider's automatic Title.
- `node web-shell.mjs <subdir>` runs the [`web-shell`](./features/web-shell.md) drive.
- `node chat-list.mjs <subdir>` runs the [`chat-list`](./features/chat-list.md) drive.
- `node activity-stream.mjs <subdir>` runs the [`activity-stream`](./features/activity-stream.md) drive. It needs `DATABASE_URL`, and the `:9090` forward for its metrics check.
- `node rename-title.mjs <subdir>` runs the [`rename-title`](./features/rename-title.md) drive.
- `node tool-calls.mjs <subdir>` runs the [`tool-calls`](./features/tool-calls.md) drive. It needs `DATABASE_URL`.
- `node s5-demo.mjs <desktop|pixel7> <subdir>` runs the [`s5-demo`](./features/s5-demo.md) end-to-end flow on one device profile.
