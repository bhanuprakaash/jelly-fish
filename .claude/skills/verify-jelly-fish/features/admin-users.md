# Admin: invites and Users (`/api/admin/*`)

An Admin invites a friend by email; the friend's first sign-in creates their User and "Personal" Project. Admins resend or revoke Invites, disable or enable Users, and make Users admins. Everyone else gets `404`.

## Sub-features

- `invite` — `POST /api/admin/invites {email}` → `201` (new) or `200` (re-sent) and an email to the invitee; `409` if the email is already a User.
- `invite-login` — the invitee's code request → code email → verify creates one workspace, User and "Personal" Project and sets `invites.accepted_at`.
- `resend` / `revoke` — `POST .../invites/{id}/resend`, `DELETE .../invites/{id}` → `204`. A revoked or expired Invite gets `202` from `POST /api/auth/code` and no email.
- `list` — `GET /api/admin/users` → `{users, invites}`.
- `disable` / `enable` — `POST .../users/{id}/disable|enable` → `204`. Disable ends the User's Login Sessions: old cookie `401`, no code emails, open streams close at the next 15 s ping.
- `make-admin` — `POST .../users/{id}/make-admin` → `204`.
- `last-admin` — disabling the only active Admin → `409`.
- `non-admin` — every `/api/admin/*` route is `404` for a non-admin (and `401` signed out).

## How to get to it (user POV)

- Settings → "Admin: Users" (admins only), or `/admin/users`: invite form, Invites (Resend, Revoke), Users (Disable/Enable, Make admin).

## Driving it with curl

Preconditions: the cluster api redeployed with this build ([`../SKILL.md`](../SKILL.md) Launch) and `$API` = `localhost:8080`; signed in as the Admin ([sign-in.md](./sign-in.md)), keeping that cookie jar. Use a throwaway address like `ver-$RANDOM@example.test`; read emails from Mailpit (`curl -s localhost:8025/api/v1/messages`, newest first).

- **Invite.** `curl -s -b admin-jar -XPOST $API/api/admin/invites -d '{"email":"…"}'` → `201`; Mailpit shows "You're invited to jelly-fish" to that address. Repeat → `200`.
- **First login.** Request a code for the invited email, read it from Mailpit, verify → `204` + cookie; `GET /api/me` shows the email. Confirm with `psql "$DATABASE_URL"`: one `users`, `workspaces` and "Personal" `projects` row, and `invites.accepted_at` set. Invite the same email again → `409`.
- **Expired / revoked.** `UPDATE invites SET expires_at = now() - interval '1 minute' …` for one, `DELETE …/invites/{id}` for another; `POST /api/auth/code` for each → `202`, Mailpit count unchanged.
- **Disable with an open stream.** As the User: `POST /api/sessions` (body `session_id`, `client_msg_id`, `message`, UUIDs), then `curl -N -b jar $API/api/sessions/{id}/events &`. As the Admin: disable the User. The stream ends within 15 s, the old cookie gets `401`, a code request sends no email; `enable` and a fresh code login works again.
- **Non-admin.** With a non-admin cookie, each route in the list above → `404`.

## Gotchas

- The shared dev DB may hold other active Admins, so the `409` last-admin case can't be driven live without disabling someone else's account; the tests cover it. Don't.
- Verification leaves `ver-*` Users and their chat sessions behind; they are harmless.
- zsh does not word-split an unquoted `$var`; split `"METHOD /path"` strings with `${r%% *}` / `${r#* }`.
- The first drive after a rollout needs the `:8080` forward restarted; a `404` on `/api/admin/users` for the Admin means the old build is still answering.
- Redact cookie values in the report.
