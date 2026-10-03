# Rename and automatic Title (`PUT /api/sessions/{id}/title`, `session.renamed`)

Every chat has a one-line Title. The Worker writes one from the first user message; the User can rename it from the Chat List, and a user rename always wins.

## Sub-features

- `auto-title`: the first Turn of a top-level chat runs one side call on the session's model, in parallel.
  - The call writes `usage.recorded` (no `turn_id`) and `session.renamed{by:auto}`, and `sessions.title` is set in the same tx.
  - There is no extra `turn.started` and no bubble. The Fake Provider's Title is the first message with whitespace collapsed, cut to 60 runes, with no `…` and no `/slow Ns` prefix.
- `rename`: the "⋯" (`Chat options`) menu on a row opens Rename.
  - An inline `Title` textbox appears with Save and Cancel; Enter saves and Esc cancels.
  - The row and the open chat's header (`h1`) update. The header updates from the `session.renamed` frame on the Session stream.
- `validation`: the title is trimmed. Empty, blank or more than 100 runes gives `400 {"error":"title must be 1-100 characters"}`, and the UI shows `role=alert` "Title must be 1–100 characters". A Child Session gives `422`. Another user's session gives `404`.
- `user-wins`: an automatic Title is never written after a `user` rename. It is dropped under the session row lock, but its usage is kept.
- `failure`: if the Title call fails, the placeholder stays, there is no retry, and the first Turn is unaffected.

## How to get to it (user POV)

- Send a first message in a new chat. The header and row switch from the placeholder to the Title.
- Hover a row and click "⋯", then Rename, type, and press Enter.

## Driving it with Playwright

Preconditions: the same as [`chat-list`](./chat-list.md).

- **Whole flow.** Run `DATABASE_URL=… JF_EVIDENCE=<dir> node rename-title.mjs <subdir>`. It does the following:
  1. Creates a chat whose first message is over 60 characters.
  2. Checks the header and row show the 60-rune Title, there is one `session.renamed{by:auto}`, `sessions.title` is set, there is one `turn.started`, the Title's `usage.recorded` is on the session, and there are 2 bubbles.
  3. Renames through the menu, checking that Save is disabled for a blank title and that 101 characters shows the alert. It then saves `"   My trip …   "`, and the row, the header and the DB show the trimmed Title with `by=user`.
  4. Sends PUT validation calls through the page's `fetch`: empty, blank and 101 characters give `400`, and 100 multi-byte runes give `204`.
  5. Inserts a child row, checks a PUT on it gives `422`, then deletes the child.
  - The event log and API answers are saved in `db-and-api.txt`.
- **Handles.**
  - Menu: `row.getByRole('button', {name: 'Chat options'})`, then `getByRole('menuitem', {name: 'Rename'})`.
  - Form: `getByRole('navigation', {name: 'Chats'}).getByRole('textbox', {name: 'Title'})`, the buttons `Save` and `Cancel`, and `getByRole('alert')`.
  - Header: `getByRole('heading', {level: 1})`.
- **Not drivable live: user rename wins the race, and Title call fails.**
  - With the Fake Provider the Title call returns at once and never fails. Racing it would mean pausing the cluster's workers, which is a shared-cluster change; don't do it from this skill.
  - These are proven by the Go tests instead: `TestUserRenameBeforeAutoLandsWins`, `TestRenameRaceNeverWritesAutoAfterUser`, `TestAutoRenameAfterUserRenameIsDroppedButUsageKept`, `TestTitleCallFailureLeavesPlaceholderAndNeverRetries` and `TestTitleErrorStaleParkDoesNotRerunTurn`.
  - Report them as test-proven, not live-proven. A real Claude Title (3–6 words) needs a real key and is left to the user.

## Gotchas

- A rename doesn't change `updated_at`, so the row doesn't move in the list.
- Other tabs see a rename only on their next list refetch (D19). The Activity Stream doesn't carry Titles.
- The rename can land while the first Turn is still streaming. This is fine: the success path doesn't re-run the Turn. Check that the event log has a single `turn.started`.
