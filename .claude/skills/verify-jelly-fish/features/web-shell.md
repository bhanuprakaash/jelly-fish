# Web shell

The Vite/React PWA (`web/`), embedded in the image and served by the api role at `/`. Signed out, it shows the login screen. Signed in, it shows the Shell: the Chat List (sidebar on desktop, its own screen on a phone) beside the open chat. `/settings` and `/admin/users` are separate pages.

## Sub-features

- `shell`: desktop shows "New chat", the `Chats` nav, the empty pane "Select a chat or start a new one", and a "Settings" link. A phone at `/` shows only the list.
- `loading`: the list reads "Loading…" until `GET /api/sessions` answers.
- `errors`: a failed list load shows "Could not load chats."; a failed `/api/me` (other than 401) shows "Could not reach the server.".
- `login`: signed out, the "Jelly-fish" heading, an `Email` field and "Email me a code".
- `routes`: `/settings` shows the "Account" heading and `/admin/users` the "Users" heading. Any other path falls back to `index.html`, and the SPA routes it.

## How to get to it (user POV)

- Open `http://localhost:8080/` in a browser.

## Driving it with Playwright

Preconditions: Launch done and `scripts/` installed.

- **Whole flow.** Run `node web-shell.mjs <subdir>`. It drives the desktop Shell, both routes, the loading and both error states, and the SPA fallback. It then checks the Pixel 7 home and a signed-out context showing the login screen.
- **Loading and error states** use `page.route` to slow down or fail one request inside the browser. Nothing on the cluster is mocked or changed, and the api still serves every other request. Say so when quoting them.
- **Served shell (curl).** `curl -si localhost:8080/` returns `200` with the embedded `index.html`.

## Gotchas

- `/api/hello` (see [hello-api](./hello-api.md)) no longer has a UI consumer. The old "hello" card is gone.
- `getByRole('heading', {name: 'Users'})` matches both the `h1` and an `h2` on `/admin/users`, so pass `level: 1`.
