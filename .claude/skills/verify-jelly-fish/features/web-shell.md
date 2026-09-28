# Web shell

The Vite/React PWA (`web/`) that renders the hello message from the api role. One build serves desktop and iOS per `web/README.md`.

## Sub-features

- `web-loading` — shows "Loading…" before the fetch resolves.
- `web-hello` — shows the greeting once `/api/hello` resolves.
- `web-error` — shows "Could not reach the API." when the fetch fails.

## How to get to it (user POV)

- Open `http://localhost:5173/` in a browser (`pnpm --dir web dev`).

## Driving it with curl / browser

Preconditions:

- The web dev server is running per [`../SKILL.md`](../SKILL.md) Launch step 4.
- The api role is running (for `web-hello`) or deliberately stopped (for `web-error`).

- **Proxy contract only (no browser tool available).** Run `curl -si localhost:5173/api/hello`. Status `200`, body `{"message":"hello"}`. This proves the data the PWA would render, not the rendered DOM.
- **Rendered proof (needs a browser tool).** Not drivable in this environment — no Playwright/browser MCP is registered. When one is added: open `http://localhost:5173/`, wait for the paragraph inside the card to stop reading "Loading…", and assert it reads exactly `hello`. Screenshot the card as evidence.
- **Error state (needs a browser tool).** Stop the api role, reload the page, and assert the same paragraph reads "Could not reach the API." Not drivable headlessly today, same reason.

## Gotchas

- Don't claim `web-hello` or `web-error` as verified from the curl-only drive — it only proves the API contract, not that React actually renders it. Report these two as "not drivable in this environment" until a browser tool is added, rather than marking them verified.
- The dev server proxies `/api`; the production build (served statically) has no such proxy and needs a real reverse proxy or same-origin deploy to behave the same way — this feature file covers dev only.
