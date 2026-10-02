# Web shell

The Vite/React PWA (`web/`) that renders the hello message from the api role. One build serves desktop and iOS per `web/README.md`.

## Sub-features

- `web-loading` — shows "Loading…" before the fetch resolves.
- `web-hello` — shows the greeting once `/api/hello` resolves.
- `web-error` — shows "Could not reach the API." when the fetch fails.

## How to get to it (user POV)

- Open `http://localhost:8080/` in a browser: the cluster api serves the PWA embedded in the image.

## Driving it with curl / browser

Preconditions:

- The cluster api is deployed and forwarded on `:8080` per [`../SKILL.md`](../SKILL.md) Launch (the image embeds the `web/` build).

- **Served shell and API contract only (no browser tool available).** Run `curl -si localhost:8080/` (status `200`, the embedded `index.html`) and `curl -si -b /tmp/jf-cj localhost:8080/api/hello` (status `200`, body `{"message":"hello"}`). This proves the page is served and the data it would render, not the rendered DOM.
- **Rendered proof (needs a browser tool).** Not drivable in this environment — no Playwright/browser MCP is registered. When one is added: open `http://localhost:8080/`, wait for the paragraph inside the card to stop reading "Loading…", and assert it reads exactly `hello`. Screenshot the card as evidence.
- **Error state (needs a browser tool).** Not drivable on the shared cluster without taking the api down; skip it.

## Gotchas

- Don't claim `web-hello` or `web-error` as verified from the curl-only drive — it only proves the API contract, not that React actually renders it. Report these two as "not drivable in this environment" until a browser tool is added, rather than marking them verified.
