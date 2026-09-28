# Hello API (`/api/hello`)

The one public endpoint: returns a static JSON greeting. It's what the web PWA shell renders.

## Sub-features

- `hello-json` — GET returns the greeting as JSON.
- `hello-via-proxy` — the same response, reached through the Vite dev proxy at `:5173`.

## How to get to it (user POV)

- Loading the PWA shell at `http://localhost:5173/` triggers a `fetch('/api/hello')` on mount (`web/src/useHello.ts`).
- Direct: `GET http://localhost:8080/api/hello`.

## Driving it with curl

Preconditions:

- The api role is running per [`../SKILL.md`](../SKILL.md) Launch step 2.

- **Direct.** Run `curl -si localhost:8080/api/hello`. Status `200`, `Content-Type: application/json`, body `{"message":"hello"}`.
- **Via web proxy.** Only if the web dev server was started (Launch step 4). Run `curl -si localhost:5173/api/hello`. Same status and body as direct — Vite's proxy config (`web/vite.config.ts`) forwards `/api` to `localhost:8080` untouched.

## Gotchas

- Compare the parsed JSON body, not raw bytes — there's no guarantee about trailing whitespace.
- If the direct call works but the proxied one doesn't, the bug is in `web/vite.config.ts`'s proxy block, not the api role — don't debug `internal/api` for a proxy-only failure.
