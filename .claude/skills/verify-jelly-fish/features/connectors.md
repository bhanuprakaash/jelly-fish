# Connectors in Settings (`/api/connectors`, Add and Remove)

A Connector is a remote MCP server of the User's project. The Connectors section of `/settings` lists them and adds or removes one (docs/design/mcp-client.md §5.8, §5.9, §8).

## Sub-features

- `list`: one row per Connector with its name, slug (mono, read-only), URL and "N of M tools on". With none: "No connectors yet."
- `add`: Name and URL, with the slug derived live from the name (lowercase letters and digits) and editable until save. Optional "Header name" and "Secret" sit under the "Uses an API key" switch.
  - Probe calls `POST /api/connectors/probe`. On success: "N tools found" and a switch per tool, all on. Save appears only after a probe, and editing the URL or key clears the probe.
  - `422` shows the api's message inline ("This address can't be used", "This server uses an outdated MCP version and can't be added"); `502` shows "could not reach the server". No Save in either case.
  - Save calls `POST /api/connectors` with the switches, and with the slug only when the User edited it. An untouched slug that is taken gets the next free number (`deepwiki2`); an edited one that is taken gets `409` and shows "That slug is taken". On success the row appears and the form resets.
- `tools`: the "N of M tools on" button expands the row; a switch calls `PATCH /api/connectors/{id}/tools/{name}`.
- `remove`: Remove opens "Remove <name>?" inline, with "Also revoke this key in <name>'s settings" when the Connector has an auth header. Cancel keeps the row; Remove calls `DELETE /api/connectors/{id}` and frees the slug.

## How to get to it (user POV)

Open `/settings` (the Account page) and scroll to Connectors.

## Driving it with Playwright

Preconditions: as [`sign-in`](./sign-in.md), and the cluster api redeployed with the Connector routes. The cluster cannot reach your laptop, so the drive adds a public no-auth MCP server over https: `https://mcp.deepwiki.com/mcp` (3 tools), or `JF_MCP_URL`.

- Check the server first: `curl -s -X POST https://mcp.deepwiki.com/mcp -H 'content-type: application/json' -H 'accept: application/json, text/event-stream' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'` answers an SSE `message` with `ask_wiki_question`, `read_wiki_contents`, `read_wiki_structure`.
- **Whole flow.** Run `node connectors.mjs <desktop|pixel7> <subdir>`. It does the following:
  1. Deletes leftover `deepwiki`, `deepwiki2` and `keycheck` Connectors, then checks the empty state.
  2. Types "Deep Wiki" (slug `deepwiki`), probes a private address (refused, no Save), then probes the real URL: "3 tools found", all on.
  3. Turns one tool off, saves, and checks the row: slug `deepwiki`, "2 of 3 tools on", and the api list (era, no auth header, one tool off).
  4. Adds a second "Deep Wiki" with the slug untouched: it is saved as `deepwiki2` (then deleted). Adds a third with the slug edited to `deepwiki`: "That slug is taken". Edits the slug to `keycheck`, adds it with header `X-API-Key` and a dummy secret, and checks the api returns the header name and never the secret.
  5. Expands the `deepwiki` row, turns the tool on ("3 of 3 tools on", api agrees, survives a reload).
  6. Removes `deepwiki` (Cancel first), then `keycheck`, which shows "Also revoke this key in Key Check's settings".
  - Touch targets are at least 44px and the page has no horizontal overflow; PNGs for each step land in the evidence dir.
- **Handles.** Inputs by label (`Name`, `Slug`, `URL`, `Header name`, `Secret`), buttons `Probe`, `Save`, `Remove`, `Cancel`, tool switches by `role=switch` named after the tool, the error by `role=alert`.

## Gotchas

- The dummy secret is sent to the public server as a header it ignores. Use a throwaway value.
- Each run leaves nothing behind when it passes; after a failed run the next run's cleanup deletes `deepwiki`, `deepwiki2` and `keycheck`.
