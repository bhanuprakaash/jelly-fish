# Hello API (`/api/hello`)

A static JSON greeting behind sign-in (`401` without a Login Session cookie).

## Sub-features

- `hello-json` — GET returns the greeting as JSON.

## How to get to it (user POV)

- Direct: `GET http://localhost:8080/api/hello`.

## Driving it with curl

Preconditions:

- The cluster api is deployed and forwarded on `:8080` per [`../SKILL.md`](../SKILL.md) Launch.
- A cookie jar from [`sign-in`](./sign-in.md) (`/tmp/jf-cj`).

- **Direct.** Run `curl -si -b /tmp/jf-cj localhost:8080/api/hello`. Without `-b`, status `401`. With it, status `200`, `Content-Type: application/json`, body `{"message":"hello"}`.

## Gotchas

- Compare the parsed JSON body, not raw bytes — there's no guarantee about trailing whitespace.
