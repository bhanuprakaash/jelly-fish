# Sign in (`/api/auth/*`, `/api/me`)

An invited email gets a 6-digit code, trades it for a Login Session cookie, and every other `/api` route accepts only that cookie.

## Sub-features

- `code-request` — `POST /api/auth/code` answers `202` with the same body for any email; only a User gets an email.
- `code-verify` — `POST /api/auth/code/verify` gives `204` + cookie for the right code, `401` for a wrong, used or expired one.
- `me` — `GET /api/me` returns the signed-in User.
- `gate` — every other `/api` route is `401` without the cookie; static assets stay public.
- Not yet mapped here: the emailed magic link (`/auth/link?t=…` → `POST /api/auth/link`), `POST /api/auth/logout`, `logout-all`, and `GET`/`DELETE /api/me/login-sessions` (Settings → Devices). Never run `logout-all` as the Admin; it ends the user's own sessions.

## How to get to it (user POV)

- Opening the PWA signed out shows the login screen: email → "Check your email" → code field.

## Driving it with curl

Preconditions:

- The cluster api is deployed per [`../SKILL.md`](../SKILL.md) Launch; its Admin comes from `JF_ADMIN_EMAIL` in `deploy/k8s/.env.app`. Below, `ADMIN` stands for that email. Mailpit is forwarded on `:8025`.

- **Request.** `curl -si -XPOST localhost:8080/api/auth/code -d '{"email":"ADMIN"}'` → `202`, `{"message":"If you're invited, a code is on its way."}`. Repeat with `nobody@example.test` → identical response, and no new Mailpit message.
- **Read the code.** `curl -s localhost:8025/api/v1/messages` — the newest message's `Subject` is `Your jelly-fish code: NNNNNN`.
- **Verify.** `curl -si -c /tmp/jf-cj -XPOST localhost:8080/api/auth/code/verify -d '{"email":"ADMIN","code":"NNNNNN"}'` → `204` and `Set-Cookie: __Host-jf_login=…; Secure` (the cluster does not set `JF_DEV_INSECURE_COOKIE`). Repeating it, or a wrong code → `401`.
- **Me.** `curl -s -b /tmp/jf-cj localhost:8080/api/me` → `{"email":"ADMIN","is_admin":true,…}`.
- **Gate.** `curl -si localhost:8080/api/me` → `401`; `curl -si localhost:8080/` → `200`.

## Gotchas

- Three codes per email per 15 minutes: a fourth request sends nothing. Clear the Mailpit inbox and wait, or use another User, when re-running a lot.
- Redact the cookie value in the report.
- The cluster cookie is `__Host-jf_login` with `Secure`; curl still sends it back to `localhost` over http (checked).
