# Activity Stream and live badges (`GET /api/activity`)

One live feed per open tab with the status of every one of the User's non-System sessions. It drives the Chat List badges and carries no chat content.

## Sub-features

- `snapshot`: the first frame (`event: snapshot`) lists the User's non-System sessions not in `awaiting_user`/`completed`.
- `status`: one `event: status` frame `{session_id, root_id, parent_id, status, needs_approval}` per `jf_activity` hint. A child's `root_id` is its top-level chat.
- `notify`: `jf_activity` is sent only on a commit holding `session.status_changed` or `session.created`. A chat started in another tab therefore reaches this tab's list (refetch on an unknown session, D19).
- `badges`: three badges, by status.
  - `role=img` `running` (spinner): `runnable`, `running`, `sleeping` or `awaiting_children`.
  - `needs approval` (amber dot): the chat or any of its children is `awaiting_approval`.
  - `failed` (red ×): no Retry; clicking the row opens the chat.
  - Priority is approval > failed > running.
- `hide-show`: on `visibilitychange` hidden, every `EventSource` closes. On visible, the Session stream reopens with `?after=<lastSeq>` and the Activity Stream reopens with a fresh snapshot.
- `cap`: 20 streams (Session + Activity) per user per api node; the 21st gets `429`. `jf_streams_open{kind="activity"|"session"}` and `jf_stream_refusals_total` are on `:9090/metrics`.

## How to get to it (user POV)

- Sign in and open `http://localhost:8080/`. The badges sit at the right of each Chat List row, inside its link.

## Driving it with Playwright

Preconditions: the same as [`chat-list`](./chat-list.md), plus the `:9090` forward (`kubectl --context jelly-fish -n jelly-fish port-forward svc/api 9090:9090`) for the metrics check.

- **Whole flow:** `DATABASE_URL=… JF_EVIDENCE=<dir> node activity-stream.mjs <subdir>`. It runs these steps:
  1. A second tab starts two `/slow 25s …` chats through `POST /api/sessions`.
  2. Both rows appear in tab 1 with `running`, then clear when the turns end.
  3. An inserted child goes to `awaiting_approval`, which puts the dot on its root; it then goes to `completed` and the dot clears.
  4. A chat is set to `failed`; the badge shows and the row opens the chat.
  5. Hide/show with a chat open:
     - every stream closes;
     - a status changed while hidden arrives through the new snapshot;
     - the URLs are `/api/sessions/<id>/events?after=<sessions.last_seq>` and `/api/activity`.
  6. Activity Streams are opened with the page's cookie until one gets `429`. Expect the page's 2 streams plus 18 extra, then a refusal. The metrics are saved at the cap.
- **Status by SQL.** The Fake Provider has no tools and never fails, so there is no product path to `awaiting_approval` or `failed` in the dev cluster.
  - The drive writes `sessions.status` and then sends the hint `Store.Append` would: `SELECT pg_notify('jf_activity', user_id||':'||id) FROM sessions WHERE id = …`.
  - The handler always re-reads the row, so this proves the stream, routing and badges. It does not prove the Worker's own transitions; those are proven by the Go tests (`TestActivityNotify`, `TestActivityStreamChildApprovalReachesRoot`).
- **Raw frames (curl):** `curl -sN -b <cookie> localhost:8080/api/activity` prints `retry: 2000`, then `event: snapshot`, then a `status` frame per change and `: ping` every 15 s.
- **Handles:** `getByRole('navigation', {name: 'Chats'}).getByRole('listitem').filter({hasText: <title>}).getByRole('img', {name: 'running' | 'needs approval' | 'failed'})`.

## Gotchas

- The `/slow Ns <text>` prefix makes the Fake Provider's reply take N seconds. The echo and the automatic Title both drop it, so match rows on `<text>`; only the brief placeholder shows the prefix.
- The page's own streams count toward the cap, so close other tabs signed in as the Admin before checking `429`.
- A status the drive sets by SQL stays in the dev DB. The drive puts `failed` back to `awaiting_user`, and the inserted child stays `completed` (it never shows in the list). The ids are written to `test-sessions.txt` in the evidence dir.
- Badge order in the list does not change on activity. The list re-sorts only on a refetch (D19).
