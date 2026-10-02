# Hello API (`/api/hello`)

A static JSON greeting behind sign-in (`401` without a Login Session cookie).

## Sub-features

- `hello-json` — GET returns the greeting as JSON.
- `hello-via-proxy` — the same response, reached through the Vite dev proxy at `:5173`.

## How to get to it (user POV)

- Loading the PWA shell at `http://localhost:5173/` triggers a `fetch('/api/hello')` on mount (`web/src/useHello.ts`).
- Direct: `GET http://localhost:8080/api/hello`.

## Driving it with curl

Preconditions:

- The api role is running per [`../SKILL.md`](../SKILL.md) Launch step 2.
- A cookie jar from [`sign-in`](./sign-in.md) (`/tmp/jf-cj`).

- **Direct.** Run `curl -si -b /tmp/jf-cj localhost:8080/api/hello`. Without `-b`, status `401`. With it, status `200`, `Content-Type: application/json`, body `{"message":"hello"}`.
- **Via web proxy.** Only if the web dev server was started (Launch step 4). Run `curl -si -b /tmp/jf-cj localhost:5173/api/hello`. Same status and body as direct — Vite's proxy config (`web/vite.config.ts`) forwards `/api` to `localhost:8080` untouched.

## Gotchas

- Compare the parsed JSON body, not raw bytes — there's no guarantee about trailing whitespace.
- If the direct call works but the proxied one doesn't, the bug is in `web/vite.config.ts`'s proxy block, not the api role — don't debug `internal/api` for a proxy-only failure.
