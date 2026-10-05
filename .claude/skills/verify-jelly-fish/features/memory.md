# Memory store and the `memory` tool

The `memory` tool (view/create/str_replace/insert/delete/rename) writes `memories` + `memory_revisions` rows and `memory.written` events (docs/design/memory.md). Memory text never lands in Worker-written events: tool args are redacted to `(memory text not stored)` and results are `memory_ref` parts.

## Sub-features

- `create`: one `memories` row at `version` 1, one `memory_revisions` row, `memory.written {memory_id, path, op, version}` next to its `tool.call.completed`.
- `view`: `view <path>` returns the content and bumps `read_count`; `view /memories` returns the live index.
- `taint`: a write after an untrusted tool (`fetch`) with no `user.message` since is saved `pending_review`, `tainted`; `view` of it is not found.
- `secret`: content with a key prefix (`sk-ant-`, `AKIA`…) is refused ("Never store secrets in memory"), nothing written.

## Driving it with curl

Preconditions as [`tool-calls`](./tool-calls.md). The Fake Provider sends a `/tool` input that is a JSON object as the call's args unchanged:

- Create: `/tool memory {"command":"create","scope":"user","path":"/memories/x.md","title":"T","kind":"fact","content":"C"}`
- View: `/tool memory {"command":"view","scope":"user","path":"/memories/x.md"}` (`scope` is required, also on `view`)
- Taint: `/tool fetch evil.example ; memory {"command":"create",...}`
- Check: `select path, version, status, tainted, read_count from memories where path like '/memories/x%'`; `select count(*) from memory_revisions where memory_id=…`; `memory.written` rows in `events`.
- Leak check: `select seq, type from events where session_id=… and payload::text like '%<content>%'` returns only the `user.message` you typed.

## Gotchas

- A `;` inside the JSON splits the call into two.
- The Fake Provider's reply echoes tool results, so its `llm.response` text can hold memory text; a real model quoting a memory does the same.
- Drives leave memories in the dev DB; list them in the report.
