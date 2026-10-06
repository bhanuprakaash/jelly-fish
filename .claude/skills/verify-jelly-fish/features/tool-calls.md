# Tool calls in the loop (`tool.call.*` events, dev fake tools)

The model can ask for tools. The Worker records every call, runs parallel-safe ones together (at most 4), runs the rest one at a time, and feeds the results to the next Turn. The cluster image is built with `TAGS=dev`, so the Fake Provider and three fake tools exist: `sleep` (parallel-safe), `fetch` (untrusted) and `slow_side_effect`.

## Sub-features

- `batch`: a message `/tool sleep 2s ; sleep 2s ; slow_side_effect 1s` gives `tool.call.requested` ×3 in one tx, `tool.call.started` ×2 (the two sleeps, same millisecond), `completed` ×2 about 2 s later, then `started` and `completed` for `slow_side_effect`, then a second `turn.started` and the reply `tools: slept 2s; slept 2s; done after 1s`.
- `interrupt`: `POST /api/sessions/{id}/interrupt` during `/tool sleep 60s ; sleep 60s` gives `tool.call.interrupted{user_interrupt}` for each started call, then `awaiting_user`. The Worker sees the interrupt on its next heartbeat, so allow `JF_HEARTBEAT` (10 s by default). The next message resumes the chat.
- `worker-kill`: covered by `make chaos` (`TestKillDuringToolCallInterruptsIt`), not driven on the cluster.

## Driving it in the browser

`DATABASE_URL=… node scripts/tool-calls.mjs tool-calls` sends the `batch` message and a `/tool nosuch x` message in two new chats, and checks the chat: a done row per call (two `sleep`, one `slow_side_effect`), each with a tinted state circle, an arg summary and a duration; a row expands to its args; a failed `nosuch` row with the danger note line `unknown tool "nosuch"`; no empty bubble where the tool-only reply is; and the `tool.call.*` events in the DB.

Tool calls are rows in one card per turn: `main .rounded-card button[aria-expanded]`. A row holds the state circle, the mono name (`span.font-mono`), a screen-reader state word (`done`, `failed`, …), the muted arg summary (`span.truncate`), a note line (`span.line-clamp-2`: running time, error) and the duration. Clicking it shows the args and result as `pre`. The summary is one of the args' values; assert it is there, not its text.

## Driving it with curl

Preconditions: signed in per [`sign-in`](./sign-in.md); `$DATABASE_URL` set; cookie header built from `$TMPDIR/jf-verify-auth.json`.

- **Start.** `POST /api/sessions` with `{"session_id":"<uuid>","client_msg_id":"<uuid>","message":"/tool sleep 2s ; sleep 2s ; slow_side_effect 1s"}` → `200`.
- **Read the log.** `psql "$DATABASE_URL" -At -c "select seq, type, to_char(created_at,'SS.MS') from events where session_id='<uuid>' order by seq"`. The two `sleep` starts share a timestamp; the third call starts after both completions.

## Gotchas

- A `/tool` input that is a JSON object is sent as the call's args unchanged (`/tool memory {"command":"view",...}`); any other input is sent as `{"input":"…"}`.

- A real Anthropic reply that asks for a tool nobody registered gets an `is_error` result (`unknown tool "x"`) and the model gets another Turn. Nothing but the Budget stops a model that keeps asking.
