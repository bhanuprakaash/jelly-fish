# Streaming (SSE): Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): a **Session** is a conversation and its agent run (never a login); the per-user status feed is the **Activity Stream**; **Jev** is shown only as "auto mode". Builds on [event-log.md](event-log.md) §5.11 (DeltaBus over `pg_notify`, 50 ms coalescing, < 8 KB), §5.12 (SSE live + replay, `Last-Event-ID` / `?after=`, deltas carry no id, per-child streams) and Decisions 6, 9, 16; [agent-loop.md](agent-loop.md) Decision 14 (callback `Stream`); [approver.md](approver.md) §8 (auto-mode wording); [agents-skills.md](agents-skills.md) §8 (child cards with Stop); [uploads-artifacts.md](uploads-artifacts.md) (blobs are fetched separately, previews on a cookie-less origin). How the stream authenticates (cookie, Origin check, expiry) is decided in the Auth and keys spec. Background and prior art: [../research/streaming.md](../research/streaming.md).

## 1. Summary

- Two kinds of SSE stream: one **Session stream** per open chat or expanded child card (event-log.md §5.12, unchanged), and one **Activity Stream** per open tab that carries only status changes for the user's sessions, so the sidebar can show badges and child approvals (D1, D2).
- The Activity Stream is fed by a new `jf_activity` NOTIFY sent in the same tx as every `session.status_changed`. On connect it sends a snapshot; it has no ids and no replay (D2, D3).
- Opening a chat replays its log from seq 0 over the Session stream. No history paging in the base version (D10).
- A slow client never blocks anyone: its deltas are dropped, and a write stuck for 10 s closes the connection (D4).
- The client closes every stream when the page is hidden and reopens with `?after=<lastSeq>` when visible, to work around iOS 18 dead streams (D6).
- The browser gets UI events, not raw log rows: a per-type allowlist serializer strips internals and never says "Jev" (D9).
- A Child Session's stream ends for good when the child ends (D7). Top-level streams never end on their own.
- Prod runs HTTPS (HTTP/2, no connection cap). Local dev stays on plain http and accepts the browser's 6-connections-per-origin limit (D11).
- Works the same in desktop and mobile browsers; each build slice is checked on a phone before it is done (D12).

## 2. Scope

**In the base version**
- `GET /sessions/{id}/events` (Session stream) with the rules in §5.1–§5.4.
- `GET /activity` (Activity Stream) and the `jf_activity` NOTIFY.
- The UI event serializer (§4.3).
- Per-client backpressure, hide/show reconnect, terminal close for Child Sessions.
- Per-user stream cap and four stream metrics (D13, D14).
- Native `EventSource` in the PWA, no client library.

**Out (deferred)**
- Paging older history ("load earlier") on chat open (D10). Revisit if a real chat takes > 2 s to open.
- Sharing one connection across tabs (BroadcastChannel / SharedWorker) (D8).
- One multiplexed per-user stream for everything (rejected in Q0; revisit only if connection limits bite in prod).
- HTTPS / HTTP/2 in local dev (D11).
- Anything Sandbox-related: the Sandbox is not in the base version, so `sandbox.*` events never reach the stream.
- The SSE auth mechanism: [auth-keys.md](auth-keys.md) §5.5 (cookie, `Sec-Fetch-Site` check, re-check on each ping).
- A native desktop app (not in product.md).

## 3. Data model

No new tables. One new NOTIFY channel:

| Channel | Payload | Sent by | When |
|---|---|---|---|
| `jf_activity` | `<user_id>:<session_id>` | `Store.Append` | same tx as every `session.status_changed` or `session.created` (event-log.md §5.1); no other append sends it (D17) |

It is a hint only, like `jf_events`: the handler always re-reads the `sessions` row.

In-memory, per API node (the **hub**, event-log.md §5.12 step 2):

```go
type client struct {
    hint   chan struct{} // cap 1: "re-query seq > lastSent"; coalesced, never dropped
    deltas chan Delta    // cap 64: dropped when full (D4)
    gapped map[string]bool // turn_ids with a dropped delta; no more deltas for them
}

type activityClient struct {
    hint chan uuid.UUID  // session ids to re-read; cap 64, on overflow send a fresh snapshot
}
```

## 4. Contracts

### 4.1 Session stream

```
GET /sessions/{id}/events        Last-Event-ID: 41   or   ?after=41
```

Frames:

```
id: 42
data: {"seq":42,"type":"tool.call.completed","created_at":"…","payload":{…UI shape…}}

event: delta
data: {"turn_id":"t7","idx":0,"kind":"text","text":"Hello wor"}

event: delta
data: {"turn_id":"t7","idx":1,"kind":"tool_start","call_id":"c1","name":"web_search","text":""}

: ping
```

- Durable frames use the default `message` event and always carry `id: <seq>`.
- Delta frames: `event: delta`, no `id`. `kind` is `text` | `thinking` | `tool_start` | `tool_args`; `call_id` and `name` only when relevant (event-log.md §5.11 `Delta`, without `SessionID`).
- First frame of every response: `retry: 2000`. `: ping` every 15 s (event-log.md §5.12).
- Response headers: `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`. No compression middleware on SSE routes. Flush after every frame.
- Status codes: `200` stream; `204` Child Session already ended and nothing newer (D7; the browser stops reconnecting); `404` not found / other tenant / deleted; `429` user already holds 20 streams on this node (D13). Same `429` rule on `/activity`.

### 4.2 Activity Stream

```
GET /activity
```

```
event: snapshot
data: [{"session_id":"…","root_id":"…","parent_id":null,"status":"running","needs_approval":false}, …]

event: status
data: {"session_id":"…","root_id":"…","parent_id":"…","status":"awaiting_approval","needs_approval":true}
```

- `snapshot` is the first frame of every response: the user's non-System sessions whose status is not `awaiting_user` or `completed` (the "busy or needs attention" set, incl. Child Sessions).
- `status` is sent on every status change of any of the user's non-System sessions, Child Sessions included. `needs_approval` = `status == "awaiting_approval"`.
- `root_id` = the top-level ancestor (follow `parent_id`, depth ≤ 2), so a child's approval badges its root chat and child card.
- No `id` field; a reconnect gets a fresh `snapshot`. Same `retry`, ping and headers as §4.1.

### 4.3 UI event serializer

`func ToUI(e Event) (UIEvent, bool)`: one function per event type; `false` = not sent. **Allowlist**: an event type without a serializer is never sent.

| Type | Sent as |
|---|---|
| `session.created` | agent name, model |
| `turn.started` | turn_id, model (drop `input_through_seq`, `tools_hash`, `app_version`) |
| `approval.requested` | approval_id, kind, tool_call_id, `by` (`auto`/`rule`/`user`/`mode`), `reason` code (`jev_unavailable` / `jev_quota_exhausted` drive the auto-mode banner) — no confidence, no thresholds |
| `tool.call.started` | tool_call_id, `approved_by` with `jev` → `auto` — no trace internals |
| `llm.response` | turn_id, message, stop_reason — no usage |
| `usage.recorded` | not sent (cost comes from `/cost`) |
| `sandbox.*` | not sent (no Sandbox in the base version) |
| every other type in event-log.md §4 | payload as stored (they carry no internals) |

Never sent on any frame: `lease_epoch`, `actor` worker ids, approver confidence, the string "Jev" (approver.md §8). `blob_ref` + preview pass through unchanged; the full blob is fetched separately (uploads-artifacts.md).

## 5. Algorithms and flows

### 5.1 Session stream handler (extends event-log.md §5.12)

```
1. authz via TenantScope (mechanism: Auth and keys)          → 404 if missing
2. after := Last-Event-ID header if present, else ?after, else 0
   non-numeric or > sessions.last_seq → after = 0            (D5)
3. if child session AND status in (completed, failed) AND after >= last_seq → 204   (D7)
4. subscribe client to hub for {id}
5. SELECT events WHERE seq > after ORDER BY seq → ToUI → send with id
6. loop:
     hint   → SELECT seq > lastSent → send; if a sent event is the child's terminal one → close
     delta  → if !gapped[turn_id] → send as event: delta
     llm.response / turn.interrupted sent for turn_id → delete gapped[turn_id]
     15 s    → ": ping" and re-query seq > lastSent
     session row gone (hard delete) → close               (reconnect then gets 404)
```

The header wins over `?after`: after an automatic reconnect the URL still holds the old `?after`, but the header holds the newer seq.

### 5.2 Backpressure (D4)

- Hub → client: `hint` has capacity 1 and is coalesced (a pending hint already means "re-query"), so durable events are never lost. `deltas` has capacity 64; on full, drop the delta and set `gapped[turn_id]`. No more deltas for that turn reach that client; the partial bubble freezes until `llm.response` replaces it.
- The hub never blocks on a client; the LISTEN reader stays fast.
- Every write uses `http.ResponseController.SetWriteDeadline(now + 10 s)`. A write that misses it closes the connection; the browser reconnects with `Last-Event-ID` and loses only deltas.

### 5.3 Activity Stream handler

```
1. authz (Auth and keys); scope = the user
2. register activityClient for user_id; upsert `user_presence` (notifications.md §5.2), again on every ping, delete on close
3. send snapshot (§4.2)
4. loop:
     hint(session_id) → read session row (+ parent chain for root_id) → skip System Sessions → send status
     hint channel overflow → send a fresh snapshot instead
     15 s → ": ping"
```

The hub's `jf_activity` listener routes by the `user_id` in the payload and ignores users with no open Activity Stream on this node.

### 5.4 Client (PWA)

```
open(chat):  es = new EventSource(`/sessions/${id}/events?after=${lastSeq ?? 0}`)
on message:  apply; lastSeq = seq
on delta:    append to the turn's partial bubble
on llm.response / turn.interrupted: replace the partial bubble
on child terminal event: es.close()
visibilitychange hidden:  close all streams (Session, children, Activity)
visibilitychange visible: reopen each with ?after=lastSeq; Activity reopens and gets a snapshot
```

- A new `EventSource` object never sends `Last-Event-ID`; only the browser's automatic reconnect does. Hence `?after=` on every manual open.
- Mid-turn reconnect: drop the partial bubble of a turn with no `llm.response`, then wait for new deltas or the final event (event-log.md §5.12).
- Per tab: 1 Activity Stream + 1 Session stream per open chat + 1 per expanded child card. Collapsing a card closes its stream.
- A `status` frame for a session the sidebar doesn't know (a chat started in another tab or device) → refetch the session list.

## 6. Rules and invariants

- Deltas are never stored and never carry an `id` (event-log.md Decision 9).
- Every durable frame's `id` equals its `seq`; the client's `lastSeq` only moves forward.
- The hub never blocks on a client; durable hints are never dropped.
- Nothing reaches the browser without a `ToUI` serializer; "Jev" never appears.
- System Sessions never appear on any stream a user can open.
- Never answer a bad `Last-Event-ID` with 4xx; `EventSource` would retry forever.
- NOTIFY payloads are hints; handlers always re-read Postgres.

## 7. Events

No new event types. New NOTIFY `jf_activity` (§3). Stream shapes in §4.

## 8. UI

- **Sidebar badges** from the Activity Stream: running (spinner; also `runnable`, `sleeping`, `awaiting_children`), needs approval (dot, also when a child of that chat needs one), failed ("Retry?"; badge only, opens the chat). `awaiting_user` and `completed` show nothing (D18).
- **Child card**: approval badge from the Activity Stream while collapsed; expanding it opens its Session stream (agents-skills.md §8).
- **Streaming bubble**: text grows with `text` deltas; thinking and tool-args deltas in their collapsible parts; `tool_start` opens a tool card titled by `name`.
- No "reconnecting" banner in the base version; reconnects are silent.

## 9. Decisions

All accepted 2026-09-27 (grilling Q0–Q13; Q2, Q3, Q6, Q8 closed in research review).

1. **Per-Session streams stay** (event-log.md §5.12), plus **one per-user Activity Stream** for status. No multiplexed stream. (Q0)
2. **Activity Stream contents**: `{session_id, root_id, parent_id, status, needs_approval}` on every status change, Child Sessions included, System Sessions never; `snapshot` on connect; no ids, no replay. (Q9)
3. **Fed by `jf_activity`** NOTIFY `<user_id>:<session_id>` in the same tx as `session.status_changed`; hint only. (Q10)
4. **Backpressure**: coalesced hints, 64-slot delta queue dropped when full (turn marked gapped), 10 s write deadline closes. (Q1)
5. **Bad `Last-Event-ID`** (non-numeric or > `last_seq`) → full replay from 0; never 400. Header wins over `?after`. (Q4)
6. **Hide/show**: the client closes all streams on `hidden` and reopens with `?after=lastSeq` on `visible`; native `EventSource`, no library. (Q11)
7. **Child stream end**: after a Child Session's terminal event the server closes and the client calls `close()`; a reconnect that is already past the end gets 204. Top-level streams never end on their own. (Q5)
8. **Several tabs**: independent streams; no tab sharing. (Q7)
9. **UI serializer**: per-type allowlist; strips traces, usage, internals; "auto mode" never "Jev". (Q12)
10. **Chat open replays from 0**; paging is Out. (Q13)
11. **Prod = HTTPS / HTTP/2; local dev = plain http** with the 6-connections-per-origin limit accepted. (Q0 follow-up)
12. **Desktop and mobile browsers are one client**; build each slice on desktop, verify it on a phone before it's done. (Follow-up)

Accepted 2026-09-28 (S1 slice grill):

15. **Phone check = Android Chrome (installed PWA)**, replacing iOS Safari for every slice. The hide/show rule (D6) stays.
16. **HTTP paths are relative to `/api`** (`GET /api/sessions/{id}/events`, `GET /api/activity`), matching the PWA proxy and service-worker denylist.

Accepted 2026-09-27 (open-gap round, Q14–Q15):

13. **Stream cap**: at most 20 open streams (Session + Activity) per user per API node; the 21st gets `429`, which also stops `EventSource` retrying.
14. **Metrics**: open streams (by kind), dropped deltas, write-deadline closes, `429` refusals; alongside `pg_notification_queue_usage()`.

Accepted 2026-10-03 (S5 slice grill):

17. **`jf_activity` on `session.status_changed` or `session.created` only**, so a chat started in another tab reaches the Chat List; no other append sends it.
18. **Busy = spinner**: `runnable`, `running`, `sleeping`, `awaiting_children` all show the spinner; no separate "retrying" badge.
19. **The Activity Stream stays status-only.** A Title change (`session.renamed`) reaches the open chat over its Session stream; other tabs see it on their next list refetch. The Chat List refetches on a `status` frame for an unknown session or one still showing its placeholder Title.
20. **Chat List**: top-level, non-System sessions, newest `updated_at` first, no paging, no Project grouping. Desktop: sidebar. Phone: its own screen; tapping a chat opens it.
21. **`user_presence` (§5.3 step 2) ships with Notifications** (S16), not with the Activity Stream.

## 10. Edge cases

- **Reconnect mid-turn**: deltas since the last durable event are gone; the partial bubble is dropped and rebuilt from new deltas or `llm.response`.
- **Delta dropped for a slow client**: that client's bubble freezes for the rest of the turn; `llm.response` fills it in.
- **Missed `jf_events` / `jf_activity` hint**: the 15 s ping re-query (Session) or the next change (Activity) catches up; the Activity Stream is also fully refreshed on every reconnect.
- **iOS 18 page returns from the background with a dead stream still "OPEN"**: the hide/show rule already closed and reopened it.
- **Two tabs, same chat**: both get every frame; each keeps its own `lastSeq`.
- **Dev: 6 connections used** (e.g. two tabs, each with a chat and child cards): the next request waits. Accepted; collapse cards or close a tab.
- **Child fails**: terminal event sent, then close; reopen gets 204. The parent's card shows the failure from `child.completed`.
- **Top-level `failed`**: stream stays open, since Retry can resume it (event-log.md §5.16).
- **Hard delete while streaming**: the next hint finds no session row → close; the reconnect gets 404 and `EventSource` stops.
- **`/model` or `/agent` mid-session**: just a `session.config_changed` frame; the stream is unaffected.
- **Event larger than 32 KB**: already stored as `blob_ref` + 2 KB preview; the frame carries that.
- **API node restart**: all streams drop; browsers reconnect with `Last-Event-ID` to any node (no sticky sessions needed: each node LISTENs to every channel).

## 11. Acceptance criteria

- Reconnect with `Last-Event-ID: N` yields exactly the UI events with `seq > N`, in order, no duplicates; delta frames have no `id` (event-log.md §9).
- A reconnect whose URL has `?after=10` and header `Last-Event-ID: 50` starts after 50.
- `Last-Event-ID: abc` and `Last-Event-ID: last_seq+100` both replay from seq 1 with status 200.
- A client that never reads: the hub keeps serving other clients with no added latency; the stuck connection is closed within ~10 s of its first blocked write; no durable event is lost after reconnect.
- With a 64-delta flood to a paused client, later deltas for that turn are not sent to it, and `llm.response` still arrives.
- A Child Session that completes: its stream sends the terminal event and closes; a new request with `?after=last_seq` gets 204.
- Activity Stream: on connect, `snapshot` lists exactly the user's non-System sessions not in `awaiting_user`/`completed`; a child entering `awaiting_approval` produces one `status` frame with the root's id as `root_id`; a Tidy memory session never appears.
- Every `session.status_changed` or `session.created` commit is followed by one `jf_activity` NOTIFY with the session's `user_id`; a rolled-back tx, or one with neither event, sends none.
- No frame contains the string "jev" (case-insensitive), `lease_epoch`, or approver confidence; `usage.recorded` never appears.
- An event type with no serializer is not sent.
- A user's 21st open stream on one node gets `429`; closing one lets the next succeed.
- Metrics expose open streams, dropped deltas, write-deadline closes and `429` refusals.
- Responses carry `Content-Type: text/event-stream`, `Cache-Control: no-cache`, `X-Accel-Buffering: no`, and no `Content-Encoding`.
- Smoke test behind the prod proxy: the first delta of a long turn reaches the browser before `llm.response` is committed.
- PWA: hiding the page closes every `EventSource`; showing it reopens each with `?after=lastSeq`; checked on Android Chrome (installed PWA) and desktop Chrome (D15).

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

SSE protocol facts, proxy buffering, Go flushing, LISTEN/NOTIFY, iOS behaviour, prior-art resume (OpenAI background mode, Vercel `resumable-stream`, LangGraph thread streams), CSRF lesson (CVE-2026-61593) and sources: [../research/streaming.md](../research/streaming.md).
