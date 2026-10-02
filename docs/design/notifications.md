# Notifications: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): a **Channel** is a way to reach a User who is away (base version: PWA push and email; never notifier/integration); an **Approval** is a paused decision; **Jev** is shown only as "auto mode". Builds on [product.md](../product.md) (Channels on an interface that carries approvals both ways), [event-log.md](event-log.md) §5.7 (approvals from other Channels use the same POST path), Decisions 4, 25, 33 (failed / background-done notifications), [approver.md](approver.md) Decisions 13, 14, 19 (one notification per stepped card, child cards, no push for Jev blocks), [streaming.md](streaming.md) D6 (streams close when the page is hidden) and §5.3 (Activity Stream), [auth-keys.md](auth-keys.md) (Login Sessions, platform secrets, email codes). No research doc: grilled from the existing specs plus one fact checked on the web (iOS/Safari web push shows no action buttons) and one from Telegram's Bot API as known (inline-button `callback_data` ≤ 64 bytes; re-check when Telegram is built).

## 1. Summary

- Base-version Channels: **PWA push** and **email**. Telegram comes right after the base version (D0).
- Two kinds: **needs you** (Approval, input request) and **FYI** (Background Session done, chat failed). Nothing else notifies (D1).
- Nobody is notified while they're looking: an open Activity Stream anywhere means "present", tracked in `user_presence` (D2).
- Needs you: push now, email if still waiting 30 min after the push. FYI: push only; email only without a push device (D3).
- One notification per chat per waiting spell; the newer push replaces the older on the device (D4).
- Lock-screen safe text: chat title, Agent, tool name, no arguments (D5). No push buttons (D6).
- Email approvals: a token link opens a page with the details and **Approve once** / **Deny**, no login needed; a GET never decides (D7).
- A transactional outbox; a dispatcher in the Worker role sends, retries, and re-checks the chat is still waiting before each send (D10).

## 2. Scope

**In the base version**
- Tables `push_subscriptions`, `user_presence`, `notifications`, `approval_links`; `users.email_notifications`.
- Web Push (VAPID, RFC 8291/8292) with a service worker; SMTP `Mailer` shared with login codes.
- Dispatcher, presence tracking, escalation, cancellation.
- Email approval page.
- Settings → Notifications.

**Out (deferred)**
- Telegram (first after the base version): bot, webhook with `X-Telegram-Bot-Api-Secret-Token`, account linking via `/start <token>`, inline Approve/Deny (payload ≤ 64 bytes). Uses the same `Channel` interface and approval resolve path.
- Push action buttons (iOS and Safari don't show them).
- Quiet hours, digests, per-Project notification settings.
- Quota warnings (usage-metering.md: none in the base version).
- WhatsApp (product.md later roadmap).

## 3. Data model

```sql
ALTER TABLE users ADD COLUMN email_notifications boolean NOT NULL DEFAULT true;

CREATE TABLE push_subscriptions (          -- one per device; "push on" for that device
  id               uuid PRIMARY KEY,
  user_id          uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  login_session_id uuid NOT NULL REFERENCES login_sessions(id) ON DELETE CASCADE, -- logout removes it
  endpoint         text NOT NULL UNIQUE,
  p256dh           text NOT NULL,
  auth             text NOT NULL,
  user_agent       text,
  created_at       timestamptz NOT NULL DEFAULT now(),
  last_ok_at       timestamptz
);

CREATE TABLE user_presence (               -- "looking at the app right now"
  user_id  uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  node_id  text NOT NULL,                  -- API node
  seen_at  timestamptz NOT NULL,
  PRIMARY KEY (user_id, node_id)
);

CREATE TABLE notifications (               -- outbox
  id          uuid PRIMARY KEY,
  user_id     uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, -- root chat
  source_id   uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, -- chat that waits (root or child)
  trigger_seq bigint NOT NULL,             -- seq of the event that started the spell, in source_id
  kind        text NOT NULL CHECK (kind IN ('needs_you','fyi')),
  reason      text NOT NULL CHECK (reason IN ('approval','input','background_done','failed')),
  channel     text NOT NULL CHECK (channel IN ('push','email')),
  send_after  timestamptz NOT NULL,
  attempts    smallint NOT NULL DEFAULT 0,
  sent_at     timestamptz,
  canceled_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (source_id, trigger_seq, channel)
);
CREATE INDEX notifications_due ON notifications (send_after) WHERE sent_at IS NULL AND canceled_at IS NULL;

CREATE TABLE approval_links (              -- email approval tokens
  token_hash  bytea PRIMARY KEY,           -- sha256(32 random bytes)
  session_id  uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE, -- the waiting chat
  approval_id text NOT NULL,
  expires_at  timestamptz NOT NULL,        -- created_at + 24 h
  used_at     timestamptz
);
```

A **waiting spell** is one stay in `awaiting_approval` or `awaiting_user`-for-input, identified by `(source_id, trigger_seq)` where `trigger_seq` is the seq of its `approval.requested` / `elicitation.requested`.

## 4. Contracts

### 4.1 Channel

```go
type Notification struct {
    UserID, SessionID, SourceID uuid.UUID
    Kind, Reason                string
    Title, Body                 string // lock-screen safe (D5)
    URL                         string // deep link into the PWA
    ApprovalLink                string // email only, reason=approval
}

type Channel interface {
    Name() string                                         // "push" | "email" (later "telegram")
    Send(ctx context.Context, n Notification) error       // ErrNoTarget = no device / email off
}

type Mailer interface { // shared with auth-keys.md §5.1
    Send(ctx context.Context, to, subject, text string) error
}
```

Answers coming back from a Channel (email page now, Telegram later) call the same `ResolveApproval` service the app's POST uses.

### 4.2 HTTP

| Method, path | Auth | Result |
|---|---|---|
| `GET /push/vapid-key` | cookie | public VAPID key |
| `POST /push/subscriptions` | cookie | `{endpoint, keys:{p256dh, auth}}` → upsert for this Login Session |
| `DELETE /push/subscriptions/current` | cookie | turn push off on this device |
| `PUT /me/notifications` | cookie | `{email: bool}` |
| `GET /a/{token}` | token | approval page (§5.5); decides nothing |
| `POST /a/{token}` | token | `{decision: "allow" \| "deny", reason?}` |

`/a/*` skips `Authn`; `http.CrossOriginProtection` still applies (the page's own form POST is same-origin).

### 4.3 Push payload

```json
{"title": "Trip planner wants approval", "body": "gmail__send · \"Paris weekend\"", "url": "/s/<root>?card=<source>", "tag": "<root session id>"}
```

TTL 24 h; `Urgency: high` for needs you, `normal` for FYI. Library: `github.com/SherClockHolmes/webpush-go`. The service worker calls `showNotification(title, {body, tag, renotify: true, data: {url}})` and, on click, focuses or opens `url`.

## 5. Algorithms and flows

### 5.1 Writing the outbox (D1, D10)

In the same tx as the trigger event:

| Trigger | Row |
|---|---|
| `approval.requested` (any kind; a child's too) | `needs_you / approval / push`, `send_after = now` |
| `elicitation.requested` | `needs_you / input / push`, `send_after = now` |
| `session.completed` with `background = true` | `fyi / background_done / push` |
| status → `failed` (crash loop, retries exhausted) | `fyi / failed / push` |

`session_id` = root of `source_id` (follow `parent_id`, depth ≤ 2). `ON CONFLICT DO NOTHING` on the unique key. Jev blocks, System Sessions and foreground replies write nothing.

### 5.2 Presence (D2)

- API node: on Activity Stream open and on every 15 s ping, `UPSERT user_presence(user_id, node_id, seen_at = now)`; on close, delete that row (only if this node has no other open Activity Stream for the User).
- `present(user) = EXISTS (seen_at > now - 30 s)`. Stale rows from a crashed node age out.

### 5.3 Dispatcher (Worker role)

```
every 5 s: SELECT … FROM notifications WHERE due FOR UPDATE SKIP LOCKED LIMIT 50
for each row:
  1. user disabled → cancel
  2. kind=needs_you and the spell is over (source's latest approval/elicitation for trigger_seq resolved) → cancel
  3. present(user):
       needs_you → send_after = now + 60 s (postpone; the badge covers it)
       fyi       → cancel
  4. send on channel:
       push:  fan out to all the User's push_subscriptions; 404/410 → delete that subscription;
              success if any delivered; none left → ErrNoTarget
       email: skip with ErrNoTarget if email_notifications = false
  5. sent → sent_at = now;
       needs_you push sent → insert email row, send_after = now + 30 min           (D3)
       push ErrNoTarget    → insert email row, send_after = now (needs_you or fyi)  (D3)
       email ErrNoTarget   → cancel
     error → attempts++, send_after = now + [30 s, 2 min, 10 min][attempts]; after 3 → cancel + log
```

The email row for a spell is inserted only by step 5, so a spell gets at most one email (D4).

### 5.4 Content (D5)

| Reason | Push title / body | Email subject |
|---|---|---|
| approval | "‹Agent› wants approval" / "‹tool› · "‹chat title›"" | "‹Agent› needs your approval" |
| input | "‹Agent› has a question" / chat title | "‹Agent› has a question" |
| background_done | "Done: ‹chat title›" / "Your background chat finished." | same |
| failed | "This chat hit an error" / "‹chat title› · Retry?" | same |

No tool arguments, no message text, never "Jev". Budget approvals read "‹Agent› reached its budget". Emails add "Review and approve" (approval link, §5.5) or "Open chat" (PWA link; login needed).

### 5.5 Email approval page (D7)

```
email build: token = 32 random bytes; insert approval_links{sha256(token), session_id: source, approval_id, expires_at: now+24h}
GET  /a/{token}: row valid (unused, unexpired, approval still open)?
       yes → page: chat title, Agent, tool, arguments (redacted like the app's card), Approve once / Deny (+ optional reason)
       no  → "This approval was already answered or has expired." + Open chat
POST /a/{token} {decision}: same checks, FOR UPDATE → ResolveApproval(scope: once, by: user:<id>, via: email)
       → used_at = now → "Done. The chat is continuing." (or "Denied.")
```

- Budget approvals: Approve = allow (one more increment, event-log.md D18), Deny.
- A stepped card with several calls: the page resolves the current call only; the next call starts a new spell and notifies again (unless the User is present).
- "Approve for this project / everywhere" is app-only (it creates an Approval Rule).
- Input requests have no token page; the email links to the chat.

### 5.6 Turning push on (D8)

1. Settings → Notifications → "Turn on for this device" (a tap, so the browser allows the prompt; on iPhone only inside the installed PWA, iOS 16.4+).
2. `Notification.requestPermission()` → `pushManager.subscribe({userVisibleOnly: true, applicationServerKey})` → `POST /push/subscriptions`.
3. One-time hint banner the first time the User runs `/background` or gets an Approval while no push device exists: "Get notified when a chat needs you. Turn on".
4. Logging out a device deletes its subscription (FK cascade from `login_sessions`).

## 6. Rules and invariants

- Every outbox row is written in the trigger's own tx; no trigger, no row.
- A needs-you notification is never sent after its spell ends.
- Nobody present gets a push or email.
- At most one push row and one email row per spell.
- Lock-screen text never includes tool arguments or message content, and never "Jev".
- A GET never resolves an Approval.
- Approval tokens are stored only as sha256 and die on use, on resolve, or at 24 h.
- VAPID keys and SMTP credentials are platform secrets (auth-keys.md §5.10).

## 7. Events

No new event types. `approval.resolved` gains optional `via` (`app` | `email`; `telegram` later) (event-log.md §4).

## 8. UI

- **Settings → Notifications**: "Push on this device" toggle (with state: on / off / blocked by the browser / "Install the app first" on iOS Safari), list of other devices with push on, "Email me when a chat needs me" toggle.
- **Hint banner**: §5.6 step 3, dismissible, once.
- **Email approval page**: minimal, no app chrome; works logged out.
- **Emails**: plain text + simple HTML; footer "Change notification settings" (PWA link).

## 9. Decisions

All accepted 2026-09-27 (grilling Q0–Q11).

0. **Base-version Channels: PWA push + email**; Telegram first after the base version. (Q0)
1. **Triggers**: needs you = Approval (incl. child, shown on the root chat), input request; FYI = Background Session done, chat failed. Nothing else. (Q1)
2. **Presence suppresses**: `user_presence` from Activity Streams; fresher than 30 s = present. (Q2)
3. **Timing**: needs you → push now, email if still waiting 30 min after the push; FYI → push only, email only without a push device. (Q3)
4. **One per chat per waiting spell**; same-tag push replaces; at most one email per spell. (Q4)
5. **Content**: chat title + Agent + tool name; no arguments; same in email. (Q5)
6. **No push action buttons** in the base version. (Q6)
7. **Email approvals**: token page (hash stored, 24 h, dies on resolve), Approve once / Deny via POST, no login; rules and input answers need the app. (Q7)
8. **Push setup**: Settings button + one-time hint; subscription tied to the Login Session; 404/410 deletes. (Q8)
9. **Settings**: push per device, email on/off (default on); quiet hours Out. (Q9)
10. **Delivery**: transactional outbox; dispatcher in the Worker role; 3 retries; re-check before send. (Q10)
11. **Email**: SMTP via a `Mailer` shared with login codes; `JF_SMTP_URL`, `JF_MAIL_FROM`. (Q11)

## 10. Edge cases

- **Approved in the app at minute 29**: the email row is canceled at step 2.
- **Present when the Approval arrives, leaves 10 min later**: the push is postponed minute by minute, then sent when presence lapses; the email follows 30 min after that push.
- **Two tabs, one hidden**: the visible tab's Activity Stream keeps the User present.
- **Node crash**: its presence rows age out after 30 s; notifications resume.
- **iPhone in Safari (not installed)**: push can't be turned on; the setting says "Install the app first"; email covers it.
- **All push subscriptions expired**: 404/410 deletes them; `ErrNoTarget` → email.
- **Email turned off and no push device**: needs-you notifications are canceled; the badge is all the User gets.
- **Email approval link opened after the chat was deleted**: the row is gone (cascade) → "already answered or expired".
- **Forwarded email**: whoever has the link can answer that one Approval once within 24 h; accepted at friends scale (the link can't create rules or reach anything else).
- **Disabled User**: nothing is sent (step 1).
- **Child Approval**: the push opens the root chat with the child's card; tag is the root, so it replaces any older push for that chat.
- **Stepped card of 3 answered by email**: each call's spell gets its own push; email follows per spell only if unanswered 30 min.

## 11. Acceptance criteria

- `approval.requested` and its outbox row commit together; a rolled-back append leaves no row.
- With a fresh `user_presence` row, no push or email is sent; the needs-you row is postponed and the FYI row canceled.
- An unanswered Approval sends one push at once and one email ~30 min later; resolving it before then cancels the email.
- A User with no push subscription gets the email immediately.
- Two pushes for the same chat carry the same `tag`.
- Push and email text never contain tool arguments or "jev" (case-insensitive) — checked with a canary argument.
- `GET /a/{token}` never changes the Approval; `POST` resolves it once, with `via: email`; a second POST shows "already answered".
- A token older than 24 h, or for a resolved Approval, is refused.
- A push endpoint returning 410 is deleted and not retried.
- Logging out a device deletes its push subscription.
- A send that fails 3 times is canceled and logged; no infinite retries.
- Jev blocks, System Sessions and foreground replies never create outbox rows.

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

None; decisions come from the specs linked in the header, grilling on 2026-09-27, and checks: iOS/Safari web push shows no action buttons (Chromium and Firefox Android do), checked 2026-09-27; Telegram `callback_data` ≤ 64 bytes, from the Bot API as known, re-check when building Telegram.
