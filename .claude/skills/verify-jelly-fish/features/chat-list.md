# Chat List (`GET /api/sessions`, sidebar / phone list)

A User's top-level, non-System chats, newest `updated_at` first. Desktop: a sidebar beside the chat. Phone: its own screen at `/`; tapping a chat opens `/s/{id}`.

## Sub-features

- `list-api`: `GET /api/sessions` returns a bare array of `{id, title, titled, status, updated_at, incognito}`. It never includes a `memory_tidy` row or a child row (`parent_id` set).
- `placeholder`: a chat with no Title shows its first user message, whitespace collapsed, cut to 60 runes + `…` (`titled: false`).
- `create`: "New chat" → first message → the row appears without a reload (the list refetches after `POST /api/sessions`).
- `open`: clicking a row opens `/s/{id}`; the Session stream starts at `?after=0` and replays the whole chat.
- `phone`: `/` is the list, a tap opens the chat full-screen, the "Back to chats" icon link and browser back return to the list.

## How to get to it (user POV)

- Sign in, open `http://localhost:8080/`. Desktop (≥ 768 px): sidebar with "New chat" on top, rows grouped under "Today" and "Earlier" headings, "Memories" and "Settings" icon links at the bottom, empty pane "Select a chat or start a new one". Phone: the same list full-screen.

## Driving it with Playwright

Preconditions: Launch done, `scripts/` installed (`npm install` in [`../scripts`](../scripts)), `DATABASE_URL` from `jellyfish_db`.

- **Whole flow.** `cd .claude/skills/verify-jelly-fish/scripts && DATABASE_URL=… JF_EVIDENCE=<dir> node chat-list.mjs <subdir>`. It prints one `PASS`/`FAIL` line per check and exits non-zero on any `FAIL`. Desktop: create two chats, check the order and keys against the DB, reopen the first and check the `?after=0` stream URL and the replayed reply. Pixel 7: list → tap → "Back to chats" → tap → browser back.
- **Handles.** `getByRole('button', {name: 'New chat'})`; `getByRole('navigation', {name: 'Chats'}).getByRole('link', {name: <title>})`; the open row has `aria-current="page"`; `getByRole('link', {name: 'Memories'|'Settings', exact: true})` (icon links, named by `aria-label`); the input is `getByPlaceholder('Message')` (placeholder "Message, or type / for commands") + `getByRole('button', {name: 'Send'})`; the phone back link is `getByRole('link', {name: 'Back to chats'})`. Fake Provider replies start with `echo:`.
- **System and child rows (curl + SQL).** Insert a `trigger='memory_tidy'` row and a child row (`parent_id` = a chat, `depth` 1) for the Admin, with `updated_at` in the future. `curl -s -b <cookie> localhost:8080/api/sessions` must not contain either id. Delete both rows afterwards. Build the cookie header from `$TMPDIR/jf-verify-auth.json`.

## Gotchas

- Reopening a chat whose turn is still streaming shows a partial bubble. The partial is dropped on reconnect and rebuilt from new deltas (streaming.md §10). Wait for the `echo: …` reply before asserting it.
- `getByText(<first message>)` also matches the `echo:` reply; pass `{ exact: true }`.
- Every run adds two chats to the Admin's list. List them in the report.
