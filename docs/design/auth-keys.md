# Auth and Keys: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): a **User** is a person with an account (never "account" in code or UI copy); a **Login Session** is one signed-in browser or device (never bare "session", which means a chat); a **Provider Key** is the user's own LLM key (never token/credential/secret). Consistent with ADR [0007](../adr/0007-own-passwordless-auth-in-go.md) (own passwordless auth in Go), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (BYOK, Quotas from day one), [mcp-client.md](mcp-client.md) §3 and Decisions 7, 24 (`JF_MASTER_KEY`, `key_id`, KMS later, `connector_credentials`), [provider-gateway.md](provider-gateway.md) Decisions 13–14 (models-list validation, only the Worker decrypts), [event-log.md](event-log.md) §5.15 and Decision 12 (`TenantScope`, no RLS; secrets never in payloads), [streaming.md](streaming.md) (SSE needs cookie auth; prod HTTPS, dev plain http). Background and prior art: [../research/auth-keys.md](../research/auth-keys.md).

## 1. Summary

- Invite-only. Sign in with an emailed 6-digit code (the same email has a link) or with Google. No passwords (D1, D5, D6, D8).
- A **Login Session** is a Postgres row; the browser holds an opaque token in the `__Host-jf_login` cookie; the DB stores only its sha256. 30 days, sliding. "Log out everywhere" deletes the rows (D2).
- CSRF: Go 1.25 `http.CrossOriginProtection` on the whole API, `SameSite=Lax`, no state-changing GETs, no CSRF tokens (D3).
- SSE streams use the same cookie, reject `Sec-Fetch-Site: cross-site`, and re-check the Login Session every 15 s ping (D4).
- The first admin comes from `JF_ADMIN_EMAIL` at startup; admins invite, list, disable, and promote Users, and set Quotas (D7–D9).
- Provider Keys and Connector credentials: one layer of AES-256-GCM with the master key, AAD ties each row to its owner, `key_id` for rotation. The API only encrypts; only the Worker decrypts (D10, D11).
- One Provider Key per Provider per User, shown as `sk-…7f3a`, never shown again (D12).

## 2. Scope

**In the base version**
- Tables `workspaces`, `users`, `projects` (minimal), `invites`, `login_codes`, `login_sessions`, `provider_keys` (§3).
- Email code + link login, Google login, logout, log out everywhere, device list.
- `http.CrossOriginProtection`, cookie auth for REST and SSE.
- Admin bootstrap, invites, admin user list, disable/enable, make admin.
- `Keyring` encryption helper shared with `connector_credentials`; master-key rotation command.
- Provider Key settings (save, replace, delete).
- Platform secrets from env / secret file (§5.9).

**Out (deferred)**
- Passkeys (ADR 0007).
- Passwords, ever, unless ADR 0007 is revisited.
- CLI login (D13). Direction for later: RFC 8628 device flow, `login_sessions.kind = 'cli'`, token in the OS keychain.
- Deleting a whole User and their data; changing a User's email.
- A hard maximum Login Session age (D16); pure 30-day sliding for now.
- Public self-serve signup.
- Cloud KMS (same `Keyring` interface, mcp-client.md D24).
- Tink, `memguard`, per-row data keys (D10, D11).
- Sandbox secrets: no Sandbox in the base version.
- Quota numbers and the admin Quota form: Usage metering spec (this spec only gives admins the right to set them).
- A User's own search key (Brave/Tavily): Web search spec; it would reuse `provider_keys` and `Keyring`.

## 3. Data model

```sql
CREATE TABLE workspaces (                 -- reserved for teams; exactly one User each for now
  id         uuid PRIMARY KEY,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE users (
  id           uuid PRIMARY KEY,
  workspace_id uuid NOT NULL REFERENCES workspaces(id),
  email        citext NOT NULL UNIQUE,     -- lowercased, trimmed
  name         text,
  google_sub   text UNIQUE,                -- set on first Google login
  is_admin     boolean NOT NULL DEFAULT false,
  disabled_at  timestamptz,                -- NULL = active
  created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE projects (                   -- minimal; build slices may add columns
  id               uuid PRIMARY KEY,
  workspace_id     uuid NOT NULL REFERENCES workspaces(id),
  user_id          uuid NOT NULL REFERENCES users(id),
  name             text NOT NULL,          -- "Personal" on first login
  instructions     text NOT NULL DEFAULT '',
  default_agent_id uuid,                   -- NULL = General (agents-skills.md D14)
  use_user_memory  boolean NOT NULL DEFAULT true,  -- memory.md §8
  created_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE invites (
  id          uuid PRIMARY KEY,
  email       citext NOT NULL,
  invited_by  uuid NOT NULL REFERENCES users(id),
  expires_at  timestamptz NOT NULL,        -- created_at + 7 days; re-send resets it
  accepted_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX invites_open ON invites (email) WHERE accepted_at IS NULL;

CREATE TABLE login_codes (                 -- one row per email sent
  id         uuid PRIMARY KEY,
  email      citext NOT NULL,
  code_hash  bytea NOT NULL,               -- sha256(6-digit code)
  link_hash  bytea NOT NULL UNIQUE,        -- sha256(32-byte random link token)
  attempts   smallint NOT NULL DEFAULT 0,  -- wrong code tries; 5 → dead
  expires_at timestamptz NOT NULL,         -- created_at + 10 min
  used_at    timestamptz,                  -- code or link, whichever first
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX login_codes_email ON login_codes (email, created_at DESC);

CREATE TABLE login_sessions (
  id           uuid PRIMARY KEY,
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   bytea NOT NULL UNIQUE,      -- sha256(32-byte random cookie token)
  kind         text NOT NULL DEFAULT 'web' CHECK (kind IN ('web')),  -- 'cli' later
  user_agent   text,                       -- for the device list
  created_at   timestamptz NOT NULL DEFAULT now(),
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  expires_at   timestamptz NOT NULL        -- last_seen_at + 30 days
);
CREATE INDEX login_sessions_user ON login_sessions (user_id);

CREATE TABLE provider_keys (
  user_id    uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  provider   text NOT NULL,                -- anthropic | openai | gemini
  ciphertext bytea NOT NULL,               -- nonce || AES-256-GCM(key), AAD = user_id|provider
  key_id     text NOT NULL,                -- which master key sealed it (mcp-client.md D24)
  last4      text NOT NULL,                -- for "sk-…7f3a"
  models     jsonb NOT NULL,               -- live models list from the save-time call (provider-gateway.md D13)
  models_fetched_at timestamptz NOT NULL,  -- refreshed daily by the Worker
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, provider)
);
```

`connector_credentials` (mcp-client.md §3) keeps its shape and uses the same `Keyring` with AAD `project_id|connector_id`.

## 4. Contracts

### 4.1 HTTP

| Method, path | Body | Result |
|---|---|---|
| `POST /api/auth/code` | `{email}` | `202` always, same message: "If you're invited, a code is on its way." |
| `POST /api/auth/code/verify` | `{email, code}` | `204` + cookie, or `401` |
| `GET /auth/link?t=…` | — | PWA page (client route, not under `/api`) with a "Sign in" button; consumes nothing |
| `POST /api/auth/link` | `{t}` | `204` + cookie, or `401` |
| `GET /api/auth/google/start` | — | redirect to Google (state, nonce, PKCE) |
| `GET /api/auth/google/callback` | — | cookie + redirect to `/`, or "Not invited" page |
| `POST /api/auth/logout` | — | deletes this Login Session |
| `POST /api/auth/logout-all` | — | deletes all of this User's Login Sessions |
| `GET /api/me` | — | `{id, email, name, is_admin}` |
| `GET /api/me/login-sessions` | — | device list (user_agent, created, last seen, "this device") |
| `DELETE /api/me/login-sessions/{id}` | — | log out that device |
| `GET /api/provider-keys` | — | `[{provider, last4, updated_at}]` |
| `PUT /api/provider-keys/{provider}` | `{key}` | validate, seal, upsert; `422 "Key rejected"` on 401/403 |
| `DELETE /api/provider-keys/{provider}` | — | delete the row |
| `POST /api/admin/invites` | `{email}` | create + email; `409` if already a User |
| `POST /api/admin/invites/{id}/resend` | — | new email, `expires_at` reset |
| `DELETE /api/admin/invites/{id}` | — | revoke |
| `GET /api/admin/users` | — | Users + open invites |
| `POST /api/admin/users/{id}/disable` / `enable` | — | §5.7 |
| `POST /api/admin/users/{id}/make-admin` | — | sets `is_admin` |

`/api/admin/*` returns `404` to non-admins.

### 4.2 Cookie

```
Set-Cookie: __Host-jf_login=<base64url 32 random bytes>; Path=/; Secure; HttpOnly; SameSite=Lax; Max-Age=2592000
```

Local http dev (`JF_DEV_INSECURE_COOKIE=1`): name `jf_login`, no `Secure`. Never set in prod.

### 4.3 Middleware

```go
// Every /api route except /api/auth/*: cookie → Login Session → User → TenantScope.
func Authn(next http.Handler) http.Handler  // 401 if missing, expired, or User disabled
func TenantScopeFrom(ctx context.Context) TenantScope  // {WorkspaceID, UserID}, event-log.md §5.15
```

Order: `http.CrossOriginProtection` → `Authn` → routes.

### 4.4 Keyring

```go
type Keyring interface {
    Seal(plaintext, aad []byte) (ct []byte, keyID string, err error) // always the primary key
    Open(ct []byte, keyID string, aad []byte) ([]byte, error)
}
```

- `JF_MASTER_KEY` (env or secret file): comma list of `id:base64(32 bytes)`; the first is primary, e.g. `m2:…,m1:…`.
- `ct` = 12-byte random nonce ‖ GCM output. `crypto/aes` + `crypto/cipher`.
- `Open` fails on a wrong `keyID`, wrong AAD, or tampering.
- A KMS implementation later satisfies the same interface.

## 5. Algorithms and flows

### 5.1 Email code (D5)

```
POST /api/auth/code {email}
1. normalize email; always answer 202
2. allowed = enabled User with that email OR open, unexpired invite
3. if !allowed → stop (no email)
4. if ≥ 3 login_codes for this email in the last 15 min → stop
5. insert login_codes{code_hash, link_hash, expires_at: now+10m}
6. email via the shared `Mailer` (notifications.md §4.1): code "482913" + link https://app/auth/link?t=<token>

POST /api/auth/code/verify {email, code}
1. row = newest unused, unexpired login_codes for email, FOR UPDATE
2. if none or attempts ≥ 5 → 401
3. if sha256(code) != code_hash → attempts++ → 401
4. used_at = now → Login (§5.3)

POST /api/auth/link {t}: row by sha256(t), unused, unexpired → used_at = now → Login (§5.3)
```

The `GET` link page never consumes the token, so mail scanners that open links don't use it up. The link logs in the browser it opens in; the code works in any browser, including an installed iOS PWA (whose cookies are separate from Safari's).

### 5.2 Google (D6)

```
start:    OIDC auth-code + PKCE, scopes "openid email", state + nonce in a short-lived cookie
callback: verify state, exchange code, verify ID token (go-oidc), require email_verified
          1. users.google_sub = sub → that User
          2. else users.email = email → set google_sub, that User
          3. else open invite for email → Login creates the User
          4. else → "Not invited" page
```

### 5.3 Login (shared)

One tx:
1. If no User exists for the email and an open invite does: create `workspaces` row, `users` row, the "Personal" Project; set `invites.accepted_at`.
2. Refuse if `users.disabled_at` is set.
3. Insert `login_sessions{token_hash: sha256(token), expires_at: now+30d, user_agent}`.
4. Set the cookie (§4.2). A new token on every login; never reuse one.

### 5.4 Each request (D2)

`sha256(cookie)` → `login_sessions` row with `expires_at > now` → User not disabled → `TenantScope`. If `last_seen_at` is older than 1 h: `last_seen_at = now, expires_at = now+30d` (at most one write per hour per Login Session).

### 5.5 SSE streams (D4, streaming.md)

- Both `GET /api/sessions/{id}/events` and `GET /api/activity` pass through `Authn`.
- Reject `Sec-Fetch-Site: cross-site` with `403`. No CORS headers are ever sent.
- On every 15 s ping, re-check the Login Session (by id, cached in the handler) and the User's `disabled_at`; if either fails, close. The browser's reconnect then gets `401` and stops; the PWA shows the login screen.

### 5.6 Admin bootstrap (D7)

At every startup: if `JF_ADMIN_EMAIL` is set and no User has that email, create workspace + User (`is_admin = true`) + "Personal" Project in one tx. Existing Users are never changed by this. The admin then logs in with an email code.

### 5.7 Disable a User (D9)

One tx: `disabled_at = now`, delete all their `login_sessions`, and append `user.interrupt{reason: user_disabled}` to each of their `running` sessions (event-log.md §5.8; propagates to Child Sessions). Parked sessions stay parked: while disabled, nothing can send them a message, approval or retry. Code and Google logins are refused. Open streams close at the next ping (§5.5). `enable` clears `disabled_at`; parked sessions stay as they were until the User acts.

### 5.8 Provider Key save (D10, D12)

```
PUT /api/provider-keys/anthropic {key}
1. call the Provider's free models-list with the plaintext (provider-gateway.md D14)
2. 401/403 → 422 "Key rejected"
3. ct, keyID = Keyring.Seal(key, aad = user_id|provider)
4. upsert provider_keys{ct, keyID, last4, models, models_fetched_at}  -- models from step 1
5. drop the plaintext; never log it; never return it
```

The API process only calls `Seal`. `Open` is called only from Worker code paths (a test asserts no `Open` call outside the worker packages). `keys rotate` is a worker-package function; the command only calls it (amended 2026-10-02).

### 5.9 Master-key rotation (D10)

1. Generate a new key, prepend it: `JF_MASTER_KEY=m2:…,m1:…`; deploy. New writes use `m2`.
2. Run `jelly-fish keys rotate` (a subcommand of the one binary): for every `provider_keys` / `connector_credentials` row with `key_id != primary`, `Open` with its key, `Seal` with the primary, update in place (one row per tx).
3. When no row has `key_id = m1`, remove `m1` from the env.

### 5.10 Platform secrets

One value per deployment, env var or secret file, never logged, never in event payloads, no `key_id`: `JF_MASTER_KEY`, `JF_ADMIN_EMAIL`, Google OAuth client id/secret, SMTP credentials, Jev (TypeSafe) key, platform search keys (Brave/Tavily), VAPID key pair, Telegram bot token.

## 6. Rules and invariants

- No passwords anywhere.
- Plaintext cookie tokens, login codes and link tokens are never stored; only sha256.
- `POST /api/auth/code` answers the same whether or not the email is invited.
- No public signup: a User is created only by an invite being used or by `JF_ADMIN_EMAIL`.
- State never changes on a GET.
- Provider Keys: sealed on save, decrypted only in the Worker, never logged, never sent to the frontend, never in the Event Log.
- Every sealed row has a `key_id` and AAD bound to its owner.
- Every repository call runs with the `TenantScope` from `Authn`.

## 7. Events

None in the Event Log (these aren't Session events). App logs record, without secrets: login success/failure (User id or email, method), logout-all, invite created/accepted, User disabled/enabled/made admin, Provider Key saved/deleted (User, Provider).

## 8. UI

- **Login screen**: email field → "Check your email" + 6-digit code field; "Continue with Google". Unknown email and invited email look identical.
- **Link page**: "Sign in to jelly-fish" button.
- **Not invited** page (Google): "This email hasn't been invited. Ask the person who runs this jelly-fish."
- **Settings → Account**: email, device list with "Log out" per device, "Log out everywhere".
- **Settings → Provider Keys**: one row per Provider: `sk-…7f3a`, Replace, Delete (confirm: "Chats using this Provider will stop until you add a key."); empty rows say "Add key". "Key rejected" on a bad key.
- **Admin → Users** (admins only): invite by email; list of Users and open invites (Resend, Revoke); per User: Disable/Enable, Make admin; Quotas (form in [usage-metering.md](usage-metering.md) §5.6).
- Any `401` → login screen.

## 9. Decisions

All accepted 2026-09-27 (grilling Q0–Q13).

0. **Build login in Go; emailed code (with link) + Google; no passwords; passkeys later** (ADR 0007). (Q0)
1. **"Login Session"** is the glossary term for a sign-in; added to CONTEXT.md. (Q1)
2. **Server-side Login Sessions**: opaque token in `__Host-jf_login` (`HttpOnly`, `Secure`, `SameSite=Lax`), sha256 in the DB, 30 days sliding, new token per login; dev http uses `jf_login` without `Secure`. (Q2)
3. **CSRF**: `http.CrossOriginProtection` (Go 1.25) + `SameSite=Lax` + no state-changing GETs; no tokens. (Q3)
4. **SSE auth**: same cookie; reject `Sec-Fetch-Site: cross-site`; no CORS; re-check every 15 s ping, close → `401` on reconnect. (Q4)
5. **Email code**: 6 digits, 10 min, single use, hashed, 5 wrong tries kills it, 3 codes / email / 15 min; link consumes only on button press. (Q5)
6. **Google**: OIDC `openid email`, `email_verified` required, matched by `sub` then email; never creates an uninvited User. (Q6)
7. **First admin from `JF_ADMIN_EMAIL`** at startup; create-only. (Q7)
8. **Invites**: admin enters an email; 7-day expiry, re-sendable, revocable; no public signup; first login creates the User and "Personal" Project. (Q8)
9. **Admin powers**: invite, list, disable/enable (kills Login Sessions), make admin, set Quotas; one `is_admin` flag. (Q9)
10. **One-layer encryption**: AES-256-GCM with the master key, AAD bound to the owner, `key_id`, `jf keys rotate` for master-key changes. No per-row data key. Replaces "envelope-encrypted" in product.md and mcp-client.md. (Q10, revised)
11. **`crypto/cipher`**, no Tink. (Q11)
12. **Provider Key UI**: one per Provider per User; `last4` shown; Replace/Delete; never shown again. (Q12)
13. **CLI login is Out**; direction recorded in §2. (Q13)

Accepted 2026-09-27 (open-gap round, Q14–Q16):

14. **Disabling a User interrupts their running sessions** (`user.interrupt{reason: user_disabled}`); parked ones stay parked. Stops platform spend at once. (Q14)
15. **Minimal `projects` table defined here** (§3); build slices may add columns. (Q15)
16. **No hard Login Session age cap** for now; Out. (Q16)

## 10. Edge cases

- **Mail scanner opens the link**: the `GET` page consumes nothing; the user's button press still works.
- **Two codes requested**: only the newest unused one verifies; older ones expire on their own.
- **Link opened in Safari from an iOS PWA user's Mail**: Safari gets signed in, not the PWA. The code is the PWA path; the email says so ("On the app? Type this code.").
- **Google redirect inside an installed iOS PWA** may finish in an in-app browser whose cookies aren't the PWA's. Verify on a real iPhone (streaming.md D12); if it fails, the email code is the PWA path.
- **Invite accepted by Google with a different Gmail**: no match → "Not invited".
- **Invite for an email that's already a User**: `409`.
- **Admin disables themselves**: allowed only if another active admin exists; otherwise refused.
- **Log out everywhere** includes the current device.
- **Master key missing or unparsable at startup**: the process refuses to start.
- **Row sealed with a `key_id` no longer in the env**: `Open` fails; the Worker treats it as `key_invalid` ("Add your key again"), agent-loop.md §5.3.
- **Provider Key deleted mid-chat**: the next turn fails `key_invalid` → `awaiting_user` with "Add key".
- **Clock skew**: all expiries use the DB clock (event-log.md D8).

## 11. Acceptance criteria

- `POST /api/auth/code` for an uninvited email returns the same `202` body as for an invited one and sends no email.
- A 4th code request within 15 min sends nothing; the 6th wrong code guess on one code returns `401` even with the right code.
- A code older than 10 min, or already used, fails; using the link consumes the code too, and vice versa.
- `GET /auth/link?t=…` leaves `used_at` NULL.
- A Google ID token with `email_verified: false` is refused; a verified uninvited email gets "Not invited" and creates no User.
- First login from an invite creates exactly one workspace, User and "Personal" Project and sets `accepted_at`.
- The DB contains no plaintext cookie token, code or link token (grep of a test dump).
- After `logout-all`, every earlier cookie of that User gets `401`, and an open SSE stream closes within 15 s.
- A cross-site `POST` (`Sec-Fetch-Site: cross-site`) to any API route is rejected; a same-origin one passes.
- A stream request with `Sec-Fetch-Site: cross-site` gets `403`.
- Startup with `JF_ADMIN_EMAIL` creates the admin once; a second startup changes nothing.
- A disabled User can't log in by code or Google, their open streams close within 15 s, and each of their `running` sessions gets `user.interrupt{reason: user_disabled}` and ends `awaiting_user`.
- `PUT /api/provider-keys/openai` with a key the models-list call rejects returns `422` and writes nothing.
- A `provider_keys` row copied to another `user_id` fails `Open` (AAD mismatch).
- After `jf keys rotate`, no row has the old `key_id`, and every key still decrypts.
- No log line, API response or event payload contains a saved Provider Key (test with a canary key).
- No `Keyring.Open` call exists outside the worker packages.

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

Login options, build vs hosted, cookie vs JWT, CSRF (incl. Go 1.25 `CrossOriginProtection`), CLI device flow, admin bootstrap, encryption, Provider key restrictions and sources: [../research/auth-keys.md](../research/auth-keys.md).
