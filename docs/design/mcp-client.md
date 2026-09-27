# MCP Client and OAuth: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Background, MCP 2026-07-28 rewrite, RFC citations and prior-art survey: [../research/mcp-client.md](../research/mcp-client.md). Consistent with ADR [0003](../adr/0003-nothing-executes-on-the-host.md) (remote Connectors called from the Worker over HTTP; local ones are Sandbox-only), ADR [0004](../adr/0004-layered-approver-no-implicit-rules.md) (layered Approver, Connector hints seed defaults), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md), [agent-loop.md](agent-loop.md) (`ToolDef`, `Tool`/`Registry` contracts, tool-list freeze §5.5, `input_required` §5.6, Decisions 4, 11, 16, 21), [event-log.md](event-log.md) (`elicitation.requested`/`.resolved` catalog rows, `awaiting_user`, payload/blob rules, `DeltaBus`, §5.18), [context.md](context.md) (prompt tool-list layer), [product.md](../product.md).

## 1. Summary

- One MCP client, built on the official Go SDK (v1.7+), that speaks both MCP eras: the current 2026-07-28 stateless revision (per-request `_meta` version, Multi Round-Trip Requests) first, falling back to the legacy `initialize`-handshake era (2025-11-25 and earlier) via the spec's own backward-compat probe.
- Remote Connectors only (Streamable HTTP, called from the Worker). Local (stdio) Connectors are deferred to the roadmap, inside the Sandbox.
- Tools only. Resources and prompts are deferred; Sampling and Roots are refused permanently (the client never declares those capabilities).
- Every tool name is prefixed with its Connector Slug (`notion__search_pages`); the prefix is stripped before the server ever sees the call.
- All enabled tools of all attached Connectors load into the prompt every turn; no on-demand tool search.
- Client identification for OAuth tries, in order: a pre-registered client ID (catalog Connectors), a Client ID Metadata Document, Dynamic Client Registration, then manual entry.
- Credentials (OAuth and static API-key/header) are stored per `(project_id, connector_id)`, encrypted with the master key like Provider Keys, decrypted only in the Worker, and keyed to the issuing auth server's `issuer`.
- Elicitation is handled both as MRTR's `input_required` (parking `awaiting_user`, resumable across a crash with an opaque `request_state`) and as the legacy in-process `elicitation/create` hold (unchanged from agent-loop.md).
- A Connector tool whose definition changes after the Connector was added asks the user again in every mode, via a content hash (rug-pull).
- Every Connector-bound HTTP call (MCP, OAuth discovery, token, revocation) goes through one SSRF-guarded HTTP client.

## 2. Scope

**In the base version**
- `internal/connector` and `internal/mcpclient` Go types; the `connectors`, `connector_tools`, `connector_credentials` tables.
- Protocol-era negotiation (modern-first, legacy-fallback) over Streamable HTTP.
- Tool-name prefixing (`<slug>__<tool>`) and Connector Slug allocation/uniqueness.
- Result content mapping onto existing `msg.Part` kinds; no new kinds.
- OAuth: RFC 9728/8414 discovery, client identification chain (pre-registered → CIMD → DCR → manual), RFC 8707 resource indicators, RFC 9207 issuer check, refresh-token handling and the cross-Worker refresh race.
- Static (non-OAuth) API-key/header credentials, stored the same way as OAuth credentials.
- `elicitation.requested`/`elicitation.resolved` field additions (`request_state`, three-way `action`); form-mode and URL-mode Elicitation.
- Rug-pull tool-hash check on every Connector tool, baseline taken when the Connector is added.
- The SSRF-guarded HTTP client used for every Connector-bound request.
- Log/payload scrubbing for Connector credential headers.
- Tool-list caching per `(project, connector)` with server `ttlMs` hints, refetched at turn start when stale.
- Add-Connector and Remove-Connector flows.
- Progress notifications on the step card via `DeltaBus`.
- Hint trust tiers: catalog vs. custom-URL Connectors.
- Error handling: `isError` vs. protocol/transport failure, `insufficient_scope`, unrecoverable login failure mid-session.

**Out (deferred)**
- Local (stdio) Connectors — roadmap item "local Connectors inside the Sandbox"; the open questions in `research/sandbox.md` §9 (process supervision across a Worker crash) and §13 (whether Connector secrets enter the Sandbox) move with it.
- Resources and prompts (prompts may later appear as slash commands).
- Sampling and Roots — refused permanently; the client never declares those capabilities.
- On-demand tool search (a `tool_search` tool) — roadmap.
- MCP `subscriptions/listen` (long-lived change notifications) — the tool list is refetched on a TTL instead.

## 3. Data model

```sql
CREATE TABLE connectors (
  id            uuid PRIMARY KEY,
  project_id    uuid NOT NULL REFERENCES projects,
  slug          text NOT NULL,             -- Connector Slug; unique within project; immutable once added
  name          text NOT NULL,
  url           text NOT NULL,             -- Streamable HTTP endpoint (or a custom-URL Connector's URL)
  catalog_key   text,                      -- NULL for a custom-URL Connector; non-NULL seeds trusted hints (Decision 18)
  client_id     text,                      -- pre-registered id, CIMD URL, or DCR-issued id (Decision 6)
  auth_mode     text NOT NULL CHECK (auth_mode IN ('oauth','api_key','none')),
  protocol_era  text NOT NULL CHECK (protocol_era IN ('modern','legacy')),  -- set at add, re-probed on a version error (Decision 27)
  created_at    timestamptz NOT NULL,
  UNIQUE (project_id, slug)
);

CREATE TABLE connector_tools (                -- the per-(project,connector) tool-list cache (Decision 15)
  connector_id  uuid NOT NULL REFERENCES connectors ON DELETE CASCADE,
  name          text NOT NULL,                -- server's own name, unprefixed
  description   text NOT NULL,
  input_schema  jsonb NOT NULL,
  annotations   jsonb,                        -- readOnlyHint/destructiveHint as the server reported them
  enabled       bool NOT NULL DEFAULT true,    -- per-tool on/off switch (Decision 16)
  ttl_ms        int,                          -- server's CacheableResult hint; default 10 min if absent
  fetched_at    timestamptz NOT NULL,
  PRIMARY KEY (connector_id, name)
);

CREATE TABLE connector_credentials (          -- OAuth and static credentials, stored identically (Decision 7)
  project_id     uuid NOT NULL,
  connector_id   uuid NOT NULL REFERENCES connectors ON DELETE CASCADE,
  issuer         text,                        -- auth server issuer; NULL for a static (non-OAuth) credential
  access_token   bytea NOT NULL,              -- encrypted with the master key; decrypted only in the Worker
  refresh_token  bytea,                       -- encrypted with the master key; NULL if none issued or not OAuth
  scopes         text[] NOT NULL DEFAULT '{}',
  key_id         text NOT NULL,               -- which master key encrypted this row (Decision 24)
  PRIMARY KEY (project_id, connector_id)
);
```

- `connectors.slug` is derived from the Connector's name, gets a number suffix if taken (`notion` → `notion2`), and is editable only while adding the Connector (Decision 3).
- `connector_tools.enabled = false` removes the tool from the prompt without dropping the cached row.
- A credential row is never reused across an `issuer` change; a changed `issuer` means re-authorize, i.e. delete and recreate the row (Decision 7).
- The master key comes from an env var / secret file (`JF_MASTER_KEY`) in dev and prod; each row stores its `key_id` so a new master key can be added without breaking old rows. A cloud KMS can replace it later behind the same interface (Decision 24). Scheme (one layer, AES-256-GCM, AAD `project_id|connector_id`): [auth-keys.md](auth-keys.md).
- `connector_tools.tool_hash`: a hash of `name + description + inputSchema + annotations`, first taken when the Connector is added to the Project (its first `tools/list`), then replaced only when the user approves the changed tool (Decision 12; §5.6). Approval Rules store no hash of their own.

## 4. Contracts

### 4.1 Connector and tool naming

```go
package connector

type AuthMode string // "oauth" | "api_key" | "none"

type Connector struct {
    ID         uuid.UUID
    ProjectID  uuid.UUID
    Slug       string   // unique within Project; immutable after creation
    Name       string
    URL        string
    CatalogKey string   // "" for a custom-URL Connector
    ClientID   string   // pre-registered id, CIMD URL, or DCR-issued id
    AuthMode   AuthMode
}

// PrefixedName builds the tool name the model sees; StripPrefix reverses it
// before the server is called (Decision 3). The server only ever sees name.
func PrefixedName(slug, name string) string { return slug + "__" + name }
func StripPrefix(slug, prefixed string) (name string, ok bool)

// ToolHash is stored on connector_tools, first at the Connector's first tools/list
// (Decision 12): sha256 over canonical-JSON {name, description, inputSchema, annotations}.
func ToolHash(name, description string, inputSchema json.RawMessage, annotations json.RawMessage) string
```

`agent-loop.md`'s `ToolDef.Source = "connector:<id>"` and `ToolDef.ReadOnly`/`Destructive` (seeded from `readOnlyHint`/`destructiveHint` only when `Connector.CatalogKey != ""`, Decision 18) are unchanged; this spec only adds the naming and hashing helpers above.

### 4.2 MCP client

```go
package mcpclient

type Client interface {
    // ListTools returns the live tool list for one Connector, speaking whichever
    // MCP era the Connector answers to (Decision 0).
    ListTools(ctx context.Context, c connector.Connector) ([]RawTool, error)

    // CallTool invokes one already-unprefixed tool. meta carries MRTR resume
    // state on a retried call; nil on a fresh call.
    CallTool(ctx context.Context, c connector.Connector, name string, args json.RawMessage, meta *ResumeMeta) (Result, error)
}

type RawTool struct {
    Name, Description string
    InputSchema        json.RawMessage
    Annotations        json.RawMessage // readOnlyHint/destructiveHint/… as reported; untrusted (Decision 18)
    TTLMs              *int            // CacheableResult hint, if the server sent one
}

type ResumeMeta struct {
    RequestState   string                   // opaque; echoed back verbatim, never parsed (Decision 10)
    InputResponses map[string]InputResponse // keyed by the server's own request key
}

type InputResponse struct {
    Action  string          // "accept" | "decline" | "cancel" (Decision 10)
    Content json.RawMessage // set only when Action == "accept" and the request had a schema
}

type Result struct {
    Content       []msg.Part     // mapped per §5.2 (Decision 5); no new msg.Part.Kind
    IsError       bool           // MCP isError:true — a normal tool error, unchanged for the model (Decision 19)
    InputRequired *InputRequired // non-nil: server returned InputRequiredResult instead of a normal result
}

type InputRequired struct {
    Requests     map[string]InputRequest // server-chosen keys, e.g. "elicitation/create"
    RequestState string                  // opaque; stored as is (Decision 10)
}

type InputRequest struct {
    Mode            string          // "form" | "url" (Decision 11)
    Message         string
    RequestedSchema json.RawMessage // form mode: flat object, primitive properties only
    URL             string          // url mode: destination the user is shown before Open
}
```

### 4.3 Credentials

```go
package connector

type Credential struct {
    ProjectID, ConnectorID uuid.UUID
    Issuer                 string   // "" for a static credential
    AccessToken            []byte   // encrypted with the master key; decrypted only in the Worker
    RefreshToken           []byte   // encrypted with the master key; nil if none issued
    Scopes                 []string
}

type CredentialStore interface {
    Get(ctx context.Context, projectID, connectorID uuid.UUID) (*Credential, error)
    Save(ctx context.Context, cred Credential) error
    Delete(ctx context.Context, projectID, connectorID uuid.UUID) error

    // Refresh runs do() under SELECT … FOR UPDATE on the credential row and
    // writes the result in the same transaction (Decision 8). A second Worker
    // calling Refresh concurrently blocks on the row, then reads the fresh
    // token instead of refreshing again.
    Refresh(ctx context.Context, projectID, connectorID uuid.UUID, do func(Credential) (Credential, error)) (*Credential, error)
}
```

### 4.4 SSRF-guarded HTTP client

```go
package connector

// NewGuardedHTTPClient is the one HTTP client used for every Connector-bound
// call: MCP requests, OAuth discovery (RFC 9728/8414), token and revocation
// endpoints (Decision 13). It requires HTTPS, blocks private/loopback/
// link-local ranges (including 169.254.169.254), re-checks the target on
// every redirect, and checks the connected IP rather than only the DNS
// answer. devAllowLocalhost is set only by a dev setting.
func NewGuardedHTTPClient(devAllowLocalhost bool) *http.Client
```

## 5. Algorithms and flows

### 5.1 Protocol-era negotiation

1. Send the request with the modern per-request `_meta` (`protocolVersion`, `clientCapabilities`: `elicitation` (form and URL) only — never `sampling`/`roots`, Decision 2, 11).
2. On HTTP 400, inspect the body: a recognized modern JSON-RPC error (`UnsupportedProtocolVersionError`, `MissingRequiredClientCapabilityError`, `HeaderMismatch`) means the server is modern but rejected this request — retry per the error; an unrecognized or empty body means the server is legacy.
3. Legacy path: send `initialize` (`protocolVersion`, `capabilities`, `clientInfo`), wait for the server's response, send `notifications/initialized`, then proceed with ordinary requests for the rest of that connection.
4. The detected era is stored in `connectors.protocol_era` at add time; later calls use it directly with no probe. A call that fails with a version error re-runs steps 1–3 and updates the stored era (Decision 27).
5. A server that answers neither probe (e.g. the deprecated 2024 HTTP+SSE transport) can't be added: "This server uses an outdated MCP version and can't be added" (Decision 22).

### 5.2 Result content mapping (Decision 5)

| MCP content | `msg.Part` |
|---|---|
| `text` | `TextPart` |
| `image` | image `Part` |
| `structuredContent` | `TextPart` holding the JSON |
| `resource_link` | `TextPart` containing the URI |
| `audio` | `TextPart` with the literal text `"[audio returned, not supported]"` |

No new `msg.Part.Kind` is added for MCP.

### 5.3 OAuth: discovery and client identification

1. A Connector call 401s with `WWW-Authenticate: Bearer resource_metadata="…"`. Fetch that Protected Resource Metadata document (RFC 9728), read `authorization_servers`, then fetch Authorization Server Metadata (RFC 8414) for the chosen one — all through the guarded HTTP client (§4.4).
2. Record the AS's `issuer` before ever redirecting the user (RFC 9207 mix-up defense); validate a returned `iss` against it before sending an authorization code to the token endpoint.
3. Client identification, in order (Decision 6):
   1. A pre-registered client ID shipped in config for a catalog Connector (`Connector.CatalogKey != ""`).
   2. A Client ID Metadata Document hosted at a public HTTPS URL — production only; skipped on localhost dev, since the auth server can't fetch it there.
   3. Dynamic Client Registration (deprecated; fallback only).
   4. Manual client ID/secret entry, shown only under an "Advanced" section.
4. If the server supports neither CIMD nor DCR: show "This Connector needs manual setup" and open the Advanced form directly.
5. Redirect with PKCE (S256) and the RFC 8707 `resource` parameter identifying this MCP server, regardless of whether the AS is known to support it.
6. On callback, exchange the code for tokens; save via `CredentialStore.Save` keyed by `(project_id, connector_id)` with the recorded `issuer`.

### 5.4 Refresh race (Decision 8)

A tool call finds the stored access token expired. It calls `CredentialStore.Refresh`, which takes `SELECT … FOR UPDATE` on the credential row, calls the auth server's token endpoint, and writes the new token in the same transaction. A second Worker calling `Refresh` for the same `(project_id, connector_id)` at the same time blocks on the row lock, then reads the token the first Worker just wrote instead of refreshing again. No Lease-style fencing is used.

### 5.5 Elicitation resume (Decision 10, 11)

1. A `CallTool` returns `Result.InputRequired`. `exec` appends `elicitation.requested{connector, schema, request_state}` and parks `awaiting_user`, releasing the Worker (agent-loop.md §5.6, event-log.md §5.18).
2. **Form mode**: the UI renders the restricted schema (flat object, primitive properties) reusing the approval UI. **URL mode**: the UI shows a confirm dialog naming the destination domain from `InputRequest.URL`, with Open and Done buttons; third-party credentials never pass through jelly-fish.
3. The user answers. `elicitation.resolved{action: accept|decline|cancel, content?}` is appended, → `runnable`.
4. Resume: the next Worker to claim the session retries the same `tool_call_id`/`idempotency_key`, with the original tool arguments unchanged, `InputResponses` keyed the same way the server asked, and the stored `RequestState` echoed byte-for-byte. The JSON-RPC id differs at the wire level; the tool call's identity at jelly-fish's abstraction level does not.
5. URL mode after Done: retry with `{action: accept}` and no content.
6. Legacy `elicitation/create`: unchanged in-process hold, up to 5 minutes, per agent-loop.md Decision 16.

### 5.6 Rug-pull check (Decision 12)

Before rules or Jev, the Approver recomputes `ToolHash` from the tool's current `name + description + inputSchema + annotations` and compares it to `connector_tools.tool_hash` (first taken when the Connector was added to the Project). A mismatch means the user is asked, in every Permission Mode and even if a rule matches, with the message "This tool changed since you approved it." Approving stores the new hash.

### 5.7 Tool-list caching (Decision 15)

- `connector_tools` is the cache for one `(project, connector)`.
- At turn start, if the cached rows are older than the server's `ttlMs` hint (default 10 minutes when none was given), call `ListTools` again and overwrite the cached rows.
- No `subscriptions/listen`. The per-turn freeze (agent-loop.md Decision 4) is unchanged: once a turn starts, its `tools_hash` is fixed even if a refetch happens mid-turn for a different reason.

### 5.8 Add-Connector flow (Decision 16)

1. Project settings → Add Connector.
2. Pick from the catalog, or paste a URL.
3. Probe: a 401 plus protected-resource metadata → show a Connect button (§5.3's OAuth flow); otherwise show an optional API-key/header field.
4. Fetch the tool list (`ListTools`), show "N tools found" with a per-tool on/off switch (`connector_tools.enabled`).
5. Save. The slug is shown and editable only in this flow.

### 5.9 Remove-Connector flow (Decision 21)

1. Best-effort revoke the credential at the auth server (OAuth only). A static API-key credential is deletion-only; the removal dialog shows "Also revoke this key in <Connector name>'s settings" (Decision 28).
2. Delete the `connector_credentials` row, whether or not revocation succeeded.
3. Delete its Approval Rules.
4. Its tools disappear starting the next turn (the current turn's frozen tool list, §5.7, is unaffected).
5. Past tool calls already in the session's history are unchanged.
6. The Connector Slug is freed for reuse.

### 5.10 Error handling

- `isError: true` in an MCP result is a normal tool error, returned to the model unchanged (Decision 19).
- A protocol or transport failure (network error, bad request, timeout) becomes `tool.call.completed{is_error: true}` with a short reason, e.g. "Notion unreachable" (Decision 19). Result size still follows the existing >32 KB blob + 2 KB preview rule (event-log.md §3); MCP results get no special handling.
- **Unrecoverable login failure mid-session** (token revoked, or refresh impossible): the tool call errors to the model, e.g. "Notion is disconnected" (Decision 9); the UI shows a Reconnect banner on the Connector. The session keeps going — chat and other tools work. No parking.
- **`insufficient_scope`**: the tool errors the same way (Decision 9's pattern), and the banner reads "needs more permission — Reconnect." Reconnect requests the union of the previously granted and newly required scopes (Decision 20). No automatic mid-chat login popup.

## 6. Rules and invariants

- The client declares only the `elicitation` capability (form and URL); `sampling` and `roots` are never declared, in either MCP era (Decision 2). The client never calls resources or prompts methods.
- A tool's server-visible name never carries the Connector Slug; stripping happens before the call leaves the client (Decision 3).
- A Connector Slug is unique within its Project, editable only during the Add-Connector flow, and immutable afterward (Decision 3).
- All enabled tools of all attached Connectors are in the prompt every turn; no on-demand tool search (Decision 4).
- Static (API-key/header) credentials are stored in the same `connector_credentials` table and encryption scheme as OAuth credentials (Decision 7).
- A credential is never reused across a changed `issuer`; a changed issuer means re-authorize (Decision 7).
- Credentials are encrypted with the master key like Provider Keys and decrypted only in the Worker (Decision 7; ADR 0003).
- `readOnlyHint`/`destructiveHint` seed the Approver's defaults and `ParallelSafe` only for catalog Connectors; a custom-URL Connector's tools default to "ask" and are not parallel-safe until a user creates an Approval Rule (Decision 18).
- A denied or errored tool call is never auto-retried; an MCP cancel (Interrupt or timeout) cancels the call's `ctx` and ignores any late response (agent-loop.md Decision 21, unchanged here).
- `request_state` is opaque: stored as is, never parsed, and never treated as a secret to scrub (Decision 10, 14).
- Every Connector-bound HTTP call — MCP, OAuth discovery, token, revocation — goes through the one guarded HTTP client (Decision 13).
- Log/payload scrubbing covers `Authorization` headers and any custom credential headers configured on a Connector; it does not cover `request_state` (Decision 14).
- A Connector tool call never parks the session except through the two Elicitation paths already defined (§5.5) or an Approval ask; an auth failure never parks — it errors to the model and shows the Reconnect banner instead (Decision 9).
- Removing a Connector deletes its credentials and Approval Rules and frees its slug, but never rewrites past tool-call history (Decision 21).

## 7. Events

No new event types. This spec fixes what the existing catalog rows (event-log.md §4) carry for Connector calls.

| Event | What this spec adds |
|---|---|
| `elicitation.requested` | gains `request_state` (opaque string, stored as is, never parsed) alongside the existing `connector`, `schema` |
| `elicitation.resolved` | `answer` becomes `{action: accept|decline|cancel, content?}` |
| `tool.call.started` / `tool.call.completed` | unchanged shape; a Connector tool's `approved_by`/`trace` and `is_error` follow the existing rules, with rug-pull (§5.6) as one more reason the Approver asks |

Progress notifications (`notifications/progress`) are never written to the Event Log; they publish to `DeltaBus` only, exactly like token deltas (event-log.md §5.11), and never extend a tool's timeout (Decision 17; agent-loop.md Decision 11).

## 8. UI

- **Add-Connector flow** (§5.8): catalog or URL entry, probe result (Connect button or API-key/header field, or "This Connector needs manual setup" opening the Advanced form), "N tools found" with a per-tool on/off switch, editable slug.
- **Outdated server**: "This server uses an outdated MCP version and can't be added" (Decision 22).
- **Removing an API-key Connector**: the dialog shows "Also revoke this key in <Connector name>'s settings" (Decision 28).
- **Advanced section**: manual client ID/secret entry, shown only when reached via the client-identification fallback (Decision 6).
- **Reconnect banner** on a Connector: shown on an unrecoverable login failure or `insufficient_scope` (Decision 9, 20); reconnecting re-runs the OAuth flow requesting the union of old and new scopes.
- **URL-mode Elicitation confirm dialog**: shows the destination domain, with Open and Done buttons (Decision 11).
- **Form-mode Elicitation**: reuses the existing approval UI to render the schema.
- **Rug-pull re-ask**: "This tool changed since you approved it," shown in place of silently reusing the old Approval Rule (Decision 12).
- **Progress**: shown live on the step card via `DeltaBus`, ephemeral, never persisted (Decision 17).

## 9. Decisions

All decided 2026-09-27.

0. **Speak both MCP eras.** Try the current 2026-07-28 stateless revision (per-request `_meta` version, MRTR) first; fall back to the legacy `initialize`-handshake era (2025-11-25 and earlier) using the spec's backward-compat probe. Use the official Go SDK (v1.7+ supports both).
1. **Remote Connectors only** in the base version (Streamable HTTP, called from the Worker). Local (stdio) Connectors go on the roadmap as "local Connectors inside the Sandbox"; sandbox research §9/§13's open questions move with them.
2. **Tools only.** Resources and prompts are deferred (prompts could later appear as slash commands). Sampling and Roots are refused permanently: the client never declares those capabilities.
3. **Tool names are prefixed with the Connector Slug**: `<slug>__<tool>` (e.g. `notion__search_pages`). The slug is derived from the Connector's name, gets a number suffix if taken (`notion2`), is unique within the Project, is editable only while adding the Connector, and is immutable after that. The prefix is stripped before calling the server, which only ever sees `search_pages`.
4. **All enabled tools of all Connectors load into the prompt.** On-demand tool search (a `tool_search` tool) goes on the roadmap.
5. **Result content mapping**: text, image and structured content map to the existing `msg` parts; `resource_link` becomes text containing the URI; audio becomes the text placeholder "[audio returned, not supported]". No new `msg.Part` kinds.
6. **Client identification order**: (1) a pre-registered client ID shipped in config for catalog Connectors → (2) a Client ID Metadata Document hosted at a public HTTPS URL (production only; skipped on localhost dev because the auth server can't fetch it) → (3) Dynamic Client Registration (deprecated, fallback) → (4) manual client ID/secret entry, shown only under an "Advanced" section. If a server supports neither (2) nor (3), show "This Connector needs manual setup" and open the Advanced form.
7. **Credentials are keyed per `(project_id, connector_id)`** in a `connector_credentials` table: access token, refresh token (if issued), granted scopes, and the auth server `issuer`. Never reuse them if the issuer changes; re-authorize instead. Encrypted with the master key like Provider Keys and decrypted only in the Worker. Master key: see Decision 24. Static API-key/header credentials (the non-OAuth path) are stored the same way.
8. **Refresh race**: `SELECT … FOR UPDATE` on the credential row; refresh and write in the same transaction; a second Worker waits, then re-reads the fresh token. No Lease-style fencing.
9. **A login failure that can't be recovered** (revoked, or refresh impossible) mid-session: the tool call returns an error to the model (e.g. "Notion is disconnected"), and the UI shows a Reconnect banner on the Connector. The session keeps going: chat and other tools work. No parking.
10. **`elicitation.requested` gains `request_state`**: opaque, stored as is, never parsed, not a secret. `elicitation.resolved` carries `{action: accept|decline|cancel, content?}`. Resume = same `tool_call_id`/`idempotency_key`, original args, `inputResponses` + the echoed `requestState`, and a new JSON-RPC request id. The legacy `elicitation/create` 5-minute in-process hold is unchanged (agent-loop.md Decision 16).
11. **Both form-mode and URL-mode elicitation are supported.** URL mode: a confirm dialog shows the destination domain with Open and Done buttons; after Done, retry with `accept` (no content). Third-party credentials never pass through jelly-fish.
12. **Rug-pull**: each Connector tool stores a hash of name + description + inputSchema + annotations, taken when the Connector is added to the Project. If a later tool list differs, the next call to that tool asks the user in every Permission Mode (full-auto included, even when a rule matches), with the message "This tool changed since you approved it." Approving stores the new hash. (Amended 2026-09-27, [approver.md](approver.md): baseline moved from the Approval Rule to the Connector tool so full-auto is covered.)
13. **SSRF**: all Connector HTTP calls (MCP, OAuth discovery, token, revocation) go through one guarded HTTP client that requires HTTPS; blocks private, loopback and link-local ranges including 169.254.169.254; re-checks every redirect; and checks the connected IP rather than only the DNS answer (no DNS rebinding). A dev setting allows localhost.
14. **Log/payload scrubbing** covers `Authorization` headers and any custom credential headers configured on a Connector. `request_state` is not scrubbed.
15. **The tool list is cached per `(project, connector)`** in the DB and refetched at turn start if older than the server's `ttlMs` hint (default 10 min if none). No `subscriptions/listen`. The per-turn freeze (agent-loop.md Decision 4) is unchanged.
16. **Add-Connector flow**: Project settings → Add Connector → pick from the catalog or paste a URL → probe (401 + protected-resource metadata → Connect button / OAuth; otherwise an optional API-key/header field) → fetch the tool list, show "N tools found" with a per-tool on/off switch → save. The slug is shown and editable only in this flow.
17. **Progress notifications** show live on the step card via the `DeltaBus` (ephemeral, never persisted). They don't extend the timeout (agent-loop.md Decision 11: 120 s default).
18. **Hints** (`readOnlyHint`/`destructiveHint`) are trusted only for catalog Connectors, where they seed the Approver defaults and `ParallelSafe`. Custom-URL Connector tools ignore hints: they default to "ask" and aren't parallel-safe until the user creates an Approval Rule.
19. **Errors**: `isError: true` is a normal tool error returned to the model. Protocol/transport failures (network, bad request, timeout) become `is_error` with a short reason (e.g. "Notion unreachable"). Result size follows the existing >32 KB blob + 2 KB preview rule and context.md truncation; MCP results aren't special.
20. **`insufficient_scope`**: the tool errors (as in Decision 9) and the banner says "needs more permission — Reconnect". Reconnect requests the union of the old and new scopes. No automatic mid-chat login popup.
21. **Removing a Connector**: delete its credentials and best-effort revoke them at the auth server (delete anyway if revoking fails); delete its Approval Rules; its tools disappear from the next turn; past tool calls in history stay unchanged; the slug is freed.
22. **No HTTP+SSE (2024-11-05) transport.** A server that only speaks it can't be added: "This server uses an outdated MCP version and can't be added."
23. **Catalog** is a `connectors.yaml` shipped with the app (name, logo, URL, optional pre-registered client ID, description), edited by the admin through a code change. Start with ~5 Connectors that have official remote MCP servers; the exact list is picked and verified when building. No admin UI for it in the base version.
24. **Master key**: env var / secret file (`JF_MASTER_KEY`) in dev and prod; every encrypted row stores a `key_id` so rotation can add keys later. A cloud KMS is a later swap behind the same interface. Covers Provider Keys too.
25. **CIMD hosting**: the API serves `GET /oauth/client.json` on the app's own domain, generated from config (app name, logo, redirect URI `https://<domain>/oauth/callback`). That URL is the client ID. No secret inside, nothing to rotate.
26. **Go SDK and parking**: the SDK's built-in MRTR helper auto-retries while holding the caller, which breaks Decision 10. Implementation starts with a 1-day spike to check whether the SDK can hand back `inputRequests`/`requestState` and accept a manual retry; if not, that one request is built by hand on the SDK's transport. The design doesn't change either way.
27. **Protocol era is stored** per Connector (`protocol_era`: `modern` | `legacy`), set at add time, and re-probed only when a call fails with a version error.
28. **Removing an API-key Connector** is deletion-only; the dialog shows "Also revoke this key in <Connector name>'s settings."

**Already decided elsewhere** (reference, don't restate as new): MCP cancel = cancel `ctx`, ignore late responses (agent-loop.md Decision 21); the external-tool gating chain (ADR 0004).

## 10. Edge cases

- **Server supports neither CIMD nor DCR**: the Add-Connector flow shows "This Connector needs manual setup" and opens the Advanced form directly (Decision 6).
- **Issuer changes for an already-connected Connector**: the stored credential is never reused; the Connector is treated as needing re-authorization.
- **Two Workers refresh the same credential at once**: the second blocks on the row lock and reads the token the first wrote; no double refresh (Decision 8).
- **Token revoked mid-session**: the in-flight and any subsequent tool call on that Connector errors to the model ("Notion is disconnected"); the Reconnect banner appears; the rest of the session is unaffected (Decision 9).
- **`insufficient_scope` on a call**: same error-and-banner pattern as a revoked token, but the banner text and the Reconnect scope request differ (Decision 20).
- **A tool's schema, description, annotations, or name changes after the Connector was added**: the stored `tool_hash` no longer matches; the next call asks the user in every mode, even if a rule matches; approving stores the new hash (Decision 12).
- **Elicitation dialog closed (✕) instead of answered**: recorded as `action: cancel` and passed to the Connector on the retry (Decision 10).
- **Elicitation `action: decline` vs `cancel`**: both are distinct from `accept` and are passed through to the Connector unchanged (Decision 10); the Connector decides what each means for its own tool.
- **Tool list older than its `ttlMs` at turn start**: refetched before the turn's tool list is frozen; once frozen, the turn uses that snapshot even if the cache changes again mid-turn (Decision 15; agent-loop.md Decision 4).
- **Removing a Connector with an in-flight tool call**: the call already started is unaffected; the Connector's tools are gone starting the next turn, and its Approval Rules and credentials are deleted (Decision 21).
- **Legacy `elicitation/create` on a Connector that also supports MRTR**: whichever era the protocol negotiation (§5.1) selected for that Connector determines which elicitation path is used; the two are not mixed within one call.
- **A Connector's OAuth redirect targets a private or loopback address**: rejected by the guarded HTTP client before the request is made, in production; allowed only under the dev localhost setting (Decision 13).
- **A catalog Connector's hints turn out to be wrong** (e.g. a destructive tool marked `readOnlyHint`): trusted anyway per Decision 18 — the spec's own caveat that hints are untrusted from non-trusted servers is the reason catalog-only trust exists, not a guarantee catalog hints are correct.

## 11. Acceptance criteria

- A Connector added by pasting a URL that returns a 400 with an unrecognized body on the modern request is retried with `initialize`, and subsequent calls to that Connector use the legacy path for the rest of the connection.
- `PrefixedName("notion", "search_pages")` returns `notion__search_pages`; `CallTool` invoked with that name calls the server with `search_pages`, never the prefixed form.
- Adding a second Connector named "Notion" in the same Project yields slug `notion2`, not a duplicate `notion`.
- A tool-result fixture containing `resource_link` content renders as a `TextPart` holding the URI; an `audio` content fixture renders as a `TextPart` reading exactly `[audio returned, not supported]`; neither adds a new `msg.Part.Kind`.
- A Connector whose auth server advertises no CIMD and no `registration_endpoint` shows "This Connector needs manual setup" and opens the Advanced form; one that advertises CIMD support skips Dynamic Client Registration entirely.
- Saving OAuth credentials for a Connector writes one `connector_credentials` row keyed by `(project_id, connector_id)`, with `issuer` set and both tokens encrypted at rest.
- A static API-key Connector's credential is stored in the same table and encryption scheme, with `issuer` NULL.
- Two concurrent `Refresh` calls for the same `(project_id, connector_id)` produce exactly one call to the auth server's token endpoint; the second caller returns the token written by the first.
- A revoked-token fixture: the tool call returns `is_error` with a message naming the Connector; the Connector's Reconnect banner appears; a chat message sent afterward in the same session still completes normally.
- An `insufficient_scope` fixture: Reconnect requests the union of the previously granted scopes and the newly required one, dropping neither.
- A rug-pull fixture: an Approval Rule created against one `inputSchema`, then the Connector's tool schema changes; the next call with that rule present is not auto-allowed, and the user sees "This tool changed since you approved it."
- Every Connector HTTP call in a test harness pointed at `169.254.169.254` or a loopback address is rejected before any bytes are sent, in production mode; the same call succeeds only when the dev localhost setting is enabled.
- A captured Connector request log contains no `Authorization` header value or configured custom credential header value in plaintext; a captured `request_state` value is left untouched.
- A Connector's cached tool list older than its `ttlMs` (or older than 10 minutes with no hint) is refetched before the next turn's `tools_hash` is computed; a refetch mid-turn does not change that turn's frozen list.
- Completing the Add-Connector flow end to end (catalog pick → probe → Connect → tool fetch → toggle one tool off → save) leaves exactly the toggled-on tools in the next turn's tool list, and the slug uneditable afterward.
- Removing a Connector deletes its `connector_credentials` row and its Approval Rules, best-effort-calls the auth server's revocation endpoint, and leaves prior `tool.call.completed` events referencing its tools unchanged in the Event Log.
- A `notifications/progress` fixture updates the step card via `DeltaBus` and produces no `events` row; the tool's 120 s timeout still fires on schedule regardless of how many progress notifications arrived.
- A custom-URL Connector's tool with `readOnlyHint: true` still defaults to "ask" and is not parallel-safe until a user creates an Approval Rule for it; the same hint on a catalog Connector's tool seeds the Approver defaults and `ParallelSafe` (Decision 18).

## 12. Open gaps

- None as of 2026-09-27 (closed by Decisions 22–28). The exact SDK surface for manual MRTR resume is verified by the Decision 26 spike at implementation start.

## 13. Research

MCP 2026-07-28 rewrite, MRTR, CIMD/DCR, RFC 9728/8414/7591/8707/9207, transport comparison, prior-art survey (Claude Code, Claude.ai Connectors, OpenAI Agents SDK, Cursor, VS Code), security (tool poisoning, rug-pull, confused deputy, SSRF), and full source list: [../research/mcp-client.md](../research/mcp-client.md).
