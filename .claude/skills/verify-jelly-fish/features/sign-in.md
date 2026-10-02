# Sign in (`/api/auth/*`, `/api/me`)

An invited email gets a 6-digit code, trades it for a Login Session cookie, and every other `/api` route accepts only that cookie.

## Sub-features

- `code-request` — `POST /api/auth/code` answers `202` with the same body for any email; only a User gets an email.
- `code-verify` — `POST /api/auth/code/verify` gives `204` + cookie for the right code, `401` for a wrong, used or expired one.
- `me` — `GET /api/me` returns the signed-in User.
- `gate` — every other `/api` route is `401` without the cookie; static assets stay public.

## How to get to it (user POV)

- Opening the PWA signed out shows the login screen: email → "Check your email" → code field.

## Driving it with curl

Preconditions:

- Migrate ran with `JF_ADMIN_EMAIL` set (Launch step 1), and the api role has `JF_SMTP_URL`, `JF_MAIL_FROM`, `JF_PUBLIC_URL` and `JF_DEV_INSECURE_COOKIE=1` (Launch step 2). Mailpit is forwarded on `:8025` / `:1025`.

- **Request.** `curl -si -XPOST localhost:8080/api/auth/code -d '{"email":"admin@example.test"}'` → `202`, `{"message":"If you're invited, a code is on its way."}`. Repeat with `nobody@example.test` → identical response, and no new Mailpit message.
- **Read the code.** `curl -s localhost:8025/api/v1/messages` — the newest message's `Subject` is `Your jelly-fish code: NNNNNN`.
- **Verify.** `curl -si -c /tmp/jf-cj -XPOST localhost:8080/api/auth/code/verify -d '{"email":"admin@example.test","code":"NNNNNN"}'` → `204` and `Set-Cookie: jf_login=…` (`jf_login` without `Secure` only because of `JF_DEV_INSECURE_COOKIE=1`). Repeating it, or a wrong code → `401`.
- **Me.** `curl -s -b /tmp/jf-cj localhost:8080/api/me` → `{"email":"admin@example.test","is_admin":true,…}`.
- **Gate.** `curl -si localhost:8080/api/me` → `401`; `curl -si localhost:8080/` → `200`.

## Gotchas

- Three codes per email per 15 minutes: a fourth request sends nothing. Clear the Mailpit inbox and wait, or use another User, when re-running a lot.
- Redact the cookie value in the report.
- The cookie jar must hold the dev cookie name `jf_login`; against a prod-config api (`__Host-jf_login`, `Secure`) curl over http will not send it back.
