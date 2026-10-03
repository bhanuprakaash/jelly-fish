# S5 demo: Chat List + Activity Stream on desktop and phone

The S5 slice end to end on one device profile:
- two chats running with live badges;
- a new chat gets its Title;
- rename it;
- hide the page while the two chats finish, then show it, and every stream recovers.

## Sub-features

- `badges-live`: two `/slow 45s …` chats started from another tab spin in the list.
- `auto-title`: a new chat's header (`h1`) and row show the automatic Title.
- `rename`: "Chat options" → Rename → Save. The row and the chat header update.
- `hide-show`: hidden closes every `EventSource`. Visible reopens the Session stream at `?after=<last_seq>` and the Activity Stream, whose fresh snapshot clears the spinners of chats that finished while hidden.
- `phone`: on `pixel7` the list and the chat are separate screens; the drive moves between them with "← Chats".

## How to get to it (user POV)

- Desktop: `http://localhost:8080/` in Chrome.
- Phone: the installed PWA on Android Chrome. The drive's `pixel7` profile is only an emulation of that.

## Driving it with Playwright

Preconditions: the same as [`chat-list`](./chat-list.md).

- Run `DATABASE_URL=… JF_EVIDENCE=<dir> node s5-demo.mjs desktop <subdir>`, then `node s5-demo.mjs pixel7 <subdir>`. Each run takes about 60 s, mostly waiting for the slow chats to finish while the page is hidden.
- Evidence: `<device>.webm`, `<device>-trace.zip`, and screenshots `two-running`, `auto-title`, `renamed-row`, `chat-open`, `after-show`.

## Gotchas

- The real-device line stays with the user: open the installed PWA on Android Chrome, background it and come back.
  - `pixel7` is desktop Chromium with a phone viewport, UA, touch and `isMobile`.
  - `setVisibility` fires `visibilitychange` by hand. It doesn't background a real app, and it doesn't reproduce the OS killing the PWA's network.
- The Fake Provider's Title is cut at 60 runes and then trimmed, so a cut that ends on a space drops it. Match on the trimmed text.
- An automatic Title drops the `/slow Ns` prefix, so match the slow chats' rows on the rest of the message.
- Every run adds three chats to the Admin's list. List them in the report.
