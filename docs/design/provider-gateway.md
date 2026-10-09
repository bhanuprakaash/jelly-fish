# Provider Gateway: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Background, prior-art survey and rejected options: [../research/provider-gateway.md](../research/provider-gateway.md). Consistent with ADR [0002](../adr/0002-neutral-message-format.md) (own neutral format, Providers are adapters), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (BYOK), [agent-loop.md](agent-loop.md) (`Provider`/`Tool`/error-class contracts, retry table), [context.md](context.md) (prompt layout, cache breakpoints, Compaction), [event-log.md](event-log.md) (payload/blob rules, `usage.recorded`, `schema_version`/Lease rule).

## 1. Summary

- Own neutral message format (`internal/msg`) behind official per-Provider SDKs (`anthropic-sdk-go`, `openai-go`, `go-genai`), each wrapped by our own adapter. No multi-provider library, no proxy: a decrypted Provider Key only ever lives in the Worker (ADR 0002, ADR 0006, product.md).
- A richer neutral format than the minimum: a file part, citations, multi-part tool results, a `NativePart` escape hatch (replayed only to the same Provider), and a `msg_v` payload version so stored messages can outlive the code that wrote them.
- One embedded model/price catalog, merged with each user's live per-key model list.
- One neutral error-class list shared by all three adapters, each carrying an action and a UI button, feeding the retry table already fixed in [agent-loop.md §5.3](agent-loop.md).
- OpenAI: Responses API only, `store:false`, full prompt resent every turn.
- Uploads always travel inline (base64); a deterministic prompt-projection rule swaps an old Upload for a short stub after the user has sent a few more messages. No Provider Files API.

## 2. Scope

**In the base version**
- `internal/msg` types, `Part.Kind`, `msg_v` and its upcasters.
- Three adapters: Anthropic, OpenAI (Responses), Gemini.
- `internal/provider/catalog/models.json` (embedded) + live per-key model merge + `GET /models`.
- Neutral error classes and their mapping from each Provider's codes.
- Stream `Delta` kinds (thinking, tool_start, tool_args), idle watchdog.
- Normalized `Usage` and `usage.recorded` emission.
- Inline uploads, the Upload projection swap rule, and the `read_upload` built-in tool.
- `Request.ResponseSchema` for internal fixed-shape calls (Tidy).
- "Show thinking" chat-header preference (UI only; never changes the request).
- Adapter test suite: request/stream/error goldens, cassettes, round-trip property test, upcaster goldens, opt-in live contract suite.

**Out (deferred)**
- Provider Files API for Uploads (only inline in the base version).
- A Chat Completions adapter (OpenAI itself, or OpenAI-compatible third parties like Ollama/GLM).
- Provider server tools other than web search/fetch (code execution, …). Search and fetch are in (Decision 6a).
- Automatic model fallback (agent-loop.md Decision 8; not revisited here).
- Any effort request parameter (each model's default is used); thinking only as Decision 17.
- Batch/priority pricing tiers.

## 3. Data model

No new Postgres tables except one column pair on `provider_keys` (§5.5); this doc defines the `internal/msg` and `internal/provider` Go types whose values are already stored in `events.payload` (event-log.md §3) and `usage` (event-log.md §3).

### 3.1 Neutral message types

```go
package msg

type Role string // "user" | "assistant" — system lives in Request.System, not a message (context.md §5.2)

type Message struct {
    MsgV  int    `json:"msg_v"`
    Role  Role   `json:"role"`
    Parts []Part `json:"parts"`
}

type Kind string // "text" | "image" | "file" | "tool_use" | "tool_result" | "thinking" | "native"
                // additive: a new Kind never bumps msg_v (Decision 1)

type Part struct {
    Kind       Kind        `json:"k"`
    Text       string      `json:"text,omitempty"`
    Citations  []Citation  `json:"cit,omitempty"`
    Blob       *BlobRef    `json:"blob,omitempty"`   // image/file part; event-log.md §3 blob_ref
    ToolUse    *ToolUse    `json:"tu,omitempty"`
    ToolResult *ToolResult `json:"tr,omitempty"`
    Thinking   *Thinking   `json:"th,omitempty"`
    Native     *Native     `json:"nat,omitempty"`
}

type ToolUse struct {
    ID     string          // ours; always set, even when the Provider gave none
    Name   string
    Args   json.RawMessage
    Opaque []byte          // Provider's own call id; same-Provider replay only
    Provider string `json:"-"` // who wrote the call; set by the Worker's fold, never stored (§5.1 item 7)
}

type ToolResult struct {
    CallID  string
    Parts   []Part // text and/or image/file parts; a structured result is a TextPart holding JSON
    IsError bool
}

type Thinking struct { // fixed shape, agent-loop.md
    Text     string
    Provider string
    Model    string
    Opaque   []byte // signature / encrypted_content; replayed exactly, same Provider only
}

type Native struct { // LangChain-style escape hatch; replayed only to the same Provider, dropped for a foreign one
    Provider string
    Type     string
    Raw      json.RawMessage
}
```

**Key legend** (Decision 2 — short keys, so stored payloads stay small):

| Key | Field | Notes |
|---|---|---|
| `k` | `Part.Kind` | text\|image\|file\|tool_use\|tool_result\|thinking\|native |
| `text` | `Part.Text` | |
| `cit` | `Part.Citations` | on text parts |
| `blob` | `Part.Blob` (`BlobRef`) | image/file content; event-log.md blob_ref shape |
| `tu` | `Part.ToolUse` | `{ID, Name, Args, Opaque}` |
| `tr` | `Part.ToolResult` | `{CallID, Parts, IsError}` |
| `th` | `Part.Thinking` | `{Text, Provider, Model, Opaque}` |
| `nat` | `Part.Native` | `{Provider, Type, Raw}` |
| `msg_v` | message version | inside every stored `msg.Message` (Decision 1) |

### 3.2 `msg_v`

Every stored `msg.Message` (in `llm.response`, `user.message`, `tool.call.completed`) carries an integer `msg_v` inside the message object. Upcasters live in `internal/msg` and run on read, the same pattern as `schema_version` (event-log.md §5.4). Adding a `Part.Kind` is additive and never bumps `msg_v`; a shape change to an existing `Kind` bumps it.

### 3.3 `provider` package types

```go
package provider

type Request struct {
    System         []TextPart
    Messages       []msg.Message
    Tools          []ToolDef       // agent-loop.md §3
    ToolChoice     ToolChoice      // {Mode: auto|none|required|one, Name}
    ResponseSchema json.RawMessage // internal fixed-shape calls only (Decision 22); never forced tool use
}

type Delta struct {
    TurnID string
    Idx    int       // neutral part index (not the Provider's own item/block index)
    Kind   DeltaKind // 0 = text (zero value; keeps today's callers correct), thinking, tool_start, tool_args
    Text   string    // text / thinking / partial tool-args JSON fragment
    CallID string    // Kind=tool_start|tool_args: our ToolUse.ID
    Name   string     // Kind=tool_start: tool name, for the card title
}

type Usage struct {
    Input, CacheRead, CacheWrite5m, CacheWrite1h, Output, Reasoning int
    ServerTools map[string]int
}

type ModelInfo struct { /* catalog view: window, max output, capability flags, prices — §7 */ }

type ErrorKind string // key_invalid|billing|model_unavailable|rate_limited|long_wait|provider_down|too_large|bug
type Error struct {
    Kind       ErrorKind
    RetryAfter time.Duration
    RequestID  string
}
```

*(Matching fields are in agent-loop.md §4.1 and event-log.md §5.11.)*

### 3.4 Catalog entry (`internal/provider/catalog/models.json`, go:embed)

Per model id: context window, max output, capability flags (vision, PDF input, structured outputs, forced `tool_choice` allowed, thinking mode: `adaptive_only` | `always_on` | `none`), prices (input, cache read, cache write 5m/1h, output, per-server-tool fees). No `source_url` field. Reviewed like code.

## 4. Contracts

### 4.1 Provider interface (additions only; base shape is agent-loop.md §4.1)

```go
type Provider interface {
    Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
    CountTokens(ctx context.Context, req Request) (int, error)
    Models() []ModelInfo                              // static catalog view — loop/Compaction thresholds
    ListModels(ctx context.Context) ([]ModelInfo, error) // live, per-key; API at key save (plaintext in hand), Worker for the daily refresh
}
```

`Models()` never changes shape or takes a key; `ListModels` is the only call that needs a key and a context. The API calls it once on key save, with the plaintext it already holds; the Worker calls it on the daily refresh (Decision 13, amended 2026-10-02).

### 4.2 `GET /models`

One endpoint, served by the API from the DB only: for each Provider the user has saved a key for, reads the stored live model list (`provider_keys.models`, §5.5), merges it with the static catalog, and returns one list. It never calls a Provider and never decrypts a key. Providers with no saved key: their models show greyed out with "Add `<Provider>` key" and cannot be selected.

### 4.3 `read_upload` tool

New built-in tool, function-schema, same on every Provider:

```
read_upload(id, pages?) -> tool result parts
```

- Text files: returns the text as a `TextPart`.
- Image/PDF: returns the file itself as tool-result parts (image/file `Part`s).
- Large PDFs: `pages` selects a page range, cut at call time by a pure-Go PDF library (timeout + memory cap); no `pages` returns the first 100 pages plus the total page count ([uploads-artifacts.md](uploads-artifacts.md)).
- The neutral part always carries our own `BlobRef`; never a Provider file id.
- Deleted Upload (`upload.deleted`): returns an error result, "File was deleted by the user".
- Over the inline budget (§5.4 item 9): returns an error result, "Too much file content in view (X MB). Ask for fewer pages." ([uploads-artifacts.md](uploads-artifacts.md) Decision 22).
- Same tool reads project files (listed in the prompt, context.md §5.2).

### 4.4 Tool result shape

`ToolResultPart{CallID, Parts []Part, IsError}` — `tool.Result.Content` is `[]msg.Part` (agent-loop.md §4.2; all three Providers accept multi-part tool results, e.g. text + image). A structured JSON result is a `TextPart` holding the JSON; the Gemini adapter wraps it as `{"output": …}` on success or `{"error": …}` when `IsError` (its `functionResponse.response` convention).

### 4.5 Tool schemas

- MCP Connector tools: strict schemas **off** (their JSON Schema often uses `pattern`/`minLength`, unsupported under strict).
- Built-in tools: strict **on**, opt-in, written in the intersection dialect (closed objects, all fields required, nullable instead of optional, no numeric/length/pattern constraints, no recursion).
- Args are always validated in Go regardless of strict mode.
- A schema-narrowing pass runs once per tool list and is cached by `tools_hash` (agent-loop.md §3), so the projected prefix stays byte-stable for caching.
- Gemini always uses `parametersJsonSchema` (JSON Schema), never the OpenAPI `parameters` path. The genai SDK re-encodes that schema, so Gemini's copy of a shared schema matches the other adapters' after key sorting, not byte for byte.

### 4.6 `ResponseSchema` (internal structured output)

`Request.ResponseSchema json.RawMessage`, used only by internal fixed-shape callers (Tidy's memory pass). Each adapter maps it to the Provider's structured-output mechanism (Anthropic `output_config.format`, OpenAI `text.format:{type:"json_schema",…,strict:true}`, Gemini `responseJsonSchema`). Never forced tool use (`tool_choice:any/tool` is rejected by Opus 5.5/Fable 5.1/Mythos 5.1). Authored in the same intersection dialect as §4.5. Always validated in Go; an invalid result is a `malformed_call`, retried once.

## 5. Algorithms and flows

### 5.1 Request build (per adapter)

1. Project `Request` from session state (context.md §5.1–§5.2; unchanged here).
2. System: `Request.System` renders as Anthropic top-level `system` (array of text blocks, cache breakpoints allowed), OpenAI a leading `developer` item (so it sits inside the cached prefix, not the separate `instructions` param), Gemini top-level `systemInstruction`.
3. Messages: neutral `Role` is `user|assistant` only. The projection never emits adjacent same-role messages or empty text; tool results and Steering text are merged into one user message before adapters ever see it (agent-loop.md).
4. Tool defs: schema-narrowing pass (§4.5), tool_choice mapped per §4.7 below.
5. Thinking replay: same-Provider history replays `Thinking.Opaque` exactly. Foreign-Provider history (after `/model`) drops all `Thinking` parts, keeping only text and tool calls (agent-loop.md Decision 6); Gemini additionally gets the dummy signature `skip_thought_signature_validator` on every function call another Provider wrote.
6. `Native` parts: replayed only when `Native.Provider` matches the destination adapter; dropped otherwise, like thinking.
7. Tool-call ids: our own ids (UUIDs) satisfy every Provider's charset. The Worker's fold tags each stored `ToolUse` in memory with the Provider of the `turn.started` that has the same `turn_id` (nothing new is stored). An adapter replays `ToolUse.Opaque` as the call id only for its own Provider; for a call written by another Provider (a `/model` switch) it uses our own id, for the call and for its result alike. No charset rewrite.
8. Uploads: always inline, per the projection rule in §5.4.

### 5.2 Streaming

Per-Provider event shapes map onto `Delta`:

| Neutral | Anthropic | OpenAI Responses | Gemini |
|---|---|---|---|
| text | `text_delta` | `output_text.delta` | new `text` part (non-thought) |
| thinking | `thinking_delta` | `reasoning_summary_text.delta` (summaries only) | `text` part with `thought:true` |
| tool_start | `content_block_start{tool_use}` | `output_item.added{function_call}` | whole `functionCall` part (start + full args together) |
| tool_args | `input_json_delta` | `function_call_arguments.delta` | synthesize one delta carrying the full args (Gemini has no partial-args streaming) |

- No usage deltas: only the final `Response.Usage` is authoritative.
- `Idx` is the neutral part index; the adapter maps Anthropic's block `index` and OpenAI's `output_index`/`content_index` onto it.
- Signature/opaque bytes are never emitted as deltas; only accumulated into the final `Response`.
- Execution uses only the final accumulated tool args, never a partial delta (agent-loop.md §6, unchanged).
- **Idle watchdog**: reset on every received stream event, including Anthropic `ping`. 300 s of silence on any of the three Providers cancels the stream with `ErrStreamIdle`, classified `provider_down` (retryable). No overall request timeout on streams — turns may legitimately run for minutes.
- **Mid-stream error**: discard the partial message entirely (tool_use/thinking blocks cannot be partially recovered); return `*llm.Error{Kind}` with the in-band error type mapped the same way an equivalent HTTP status would be.

### 5.3 Response and usage

1. Accumulate the stream into `msg.Message` + `StopReason` + `Usage` + `RequestID`.
2. Normalize `Usage.Input` to "uncached, non-written input" so the three Providers add up the same way: Anthropic's `input_tokens` already excludes cache; OpenAI subtracts `input_tokens_details.cached_tokens` and `cache_write_tokens`; Gemini subtracts `cachedContentTokenCount` from `promptTokenCount`.
3. `exec` writes one `usage.recorded` event per non-zero `Usage` class (`kind=llm`, `provider`, `model` ([usage-metering.md](usage-metering.md) D5), `unit=input_tokens|cache_read_tokens|cache_write_5m_tokens|cache_write_1h_tokens|output_tokens|reasoning_tokens|…`), in the same transaction as `llm.response`. This fits the `UNIQUE(session_id, seq)` shape in event-log.md §3 — one row per event.
4. Thinking/reasoning tokens are a detail inside `Output`, never billed as a second class. Gemini counts them apart from `candidatesTokenCount` (live, gemini-3.8-flash: `thoughtsTokenCount` 95, `candidatesTokenCount` 4 for a 4-character answer), so `Output` is `candidatesTokenCount + thoughtsTokenCount`.
5. `cost_micros` is computed from the catalog at record time and never recalculated; a later price-table fix never rewrites history (event-log.md §5.9 Budget reads `cost_micros`).

### 5.4 Upload projection swap rule

1. An Upload attached to a user message starts, and stays, inline (full base64 content) for the next **N=3** user messages after the one it was attached to.
2. After that, at the **first user-message boundary** on or after N=3 (or at **Compaction**, if that happens first), the projection replaces the Upload's content with a stub note in its place, e.g.:
   ```
   [report.pdf, 42 pages, 3 MB. Removed from view; call read_upload("up_7") to read it]
   ```
3. The swap never happens **mid tool loop** (i.e. never between a tool call and its result) — only at a user-message boundary or at Compaction.
4. The Event Log itself never changes: the swap is a deterministic projection rule, replay-exact from the stored events and the user-message count. It is **not** an event.
5. Accepted cost: one cache break at the swap point (the prefix byte-changes once).
6. If the agent needs the Upload again after the swap, it calls `read_upload(id, pages?)` (§4.3), which returns fresh content as tool-result parts.
7. `N` is tunable later with evals; 3 is the starting value.
8. **Deleted Upload**: after `upload.deleted`, the next projection swaps it right away (no N wait) for `[report.pdf was deleted by the user]`.
9. **Inline budget**: total base64 size of inline Uploads in the projected prompt is capped at 24 MB (tunable). Over the cap, the oldest inline Uploads are stubbed early (before their N=3 turns are up), oldest first, until back under budget. Deterministic, a pure function of events, same as the rest of this rule ([uploads-artifacts.md](uploads-artifacts.md)).
10. **`read_upload` results count toward the same budget** and are stubbed after 3 user messages, oldest first, by the same rule as inline Uploads ([uploads-artifacts.md](uploads-artifacts.md)).

### 5.5 Key validation and model catalog merge

1. **On saving a Provider Key**: call that Provider's free models-list endpoint only (no paid probe). Failure (401/403) rejects the key immediately in settings. Success stores the returned list in `provider_keys.models jsonb` + `models_fetched_at`, which seeds the `/model` picker.
2. Billing problems (no credit, spend cap) are not caught here — they only surface on the first real turn, as a `billing` error (§9).
3. **At Worker start and daily**: refresh each saved key's `provider_keys.models`, and merge live limits (Anthropic's list endpoint only) into the static catalog. On a conflict, prefer the live value; log the diff. Gemini and OpenAI list endpoints are not merged: `models.json` is their source.
4. **Unknown model id** (not in the catalog): allowed, with conservative default limits and "price unknown" shown in the UI; no dollar amount is computed for it.

### 5.6 Error classification

Each adapter maps its own HTTP status / error code / stream error onto the one shared class list (§9 Decision — table there). Unknown codes: any 5xx → `provider_down`; anything else unmapped → `bug`. The request id is always captured (`Response.RequestID` / error response header) and attached to `session.error` for support.

## 6. Rules and invariants

- A `Native` part is replayed only to the Provider named in `Native.Provider`; every other adapter drops it silently. The core never interprets its contents.
- `ThinkingPart`/`Native` opaque bytes are never emitted as stream deltas, only accumulated into the final `Response`.
- A neutral `Part` for an Upload always carries our own `BlobRef`; never a Provider file id, because no Files API is used in the base version.
- Because uploads are never sent through a Files API, hard delete needs no Provider-side cleanup for them (event-log.md §5.13 already assumes this).
- The Upload projection swap is a pure function of (attach turn, current user-message count, latest Compaction); it is never persisted as its own event and never happens mid tool loop.
- `cost_micros` is fixed at `usage.recorded` time from the catalog then in effect; a later catalog price edit never rewrites past `usage` rows.
- `Models()` stays the static, keyless catalog view used by the loop and Compaction thresholds; only `ListModels(ctx)` touches a key: from the API with the plaintext being saved, otherwise only from the Worker. The API never decrypts a stored key.
- The only Provider server tools used are web search and web fetch (Decision 6a); no code execution or others.
- OpenAI always uses the Responses API with `store:false`; the full projected prompt is resent every turn; `previous_response_id` is never used.
- `tool_choice` `required`/`one` on a model whose catalog entry has `forced_tool_choice=false` is rejected by the adapter before sending, not by the Provider.
- The stream idle watchdog resets on every event (including Anthropic `ping`); firing is always classified `provider_down`, never a different class.
- A Worker that reads an unknown `msg_v` or an unknown `Part.Kind` releases the Lease immediately without running `Decide` (event-log.md §5.19's rule, applied here).
- The "Show thinking" preference is pure UI: the request sent to the Provider never changes because of it; cache and replay are unaffected and it adds no cost.
- No effort parameter is ever sent. The only thinking parameter is `thinking:{type:"adaptive", display:"summarized"}` on catalog `adaptive_only` models (Decision 17, amended 2026-10-02).

## 7. Events

This spec does not add new event types; it fixes what already-catalogued events carry (event-log.md §4).

| Event | What this spec adds |
|---|---|
| `llm.response` | `message` is a `msg.Message` with top-level `msg_v`; `stop_reason` includes `context_exceeded` for Anthropic's `model_context_window_exceeded` (§9 Decision, not an error) |
| `user.message` | `message` carries `msg_v`; Upload refs are the same `BlobRef`s the projection later swaps for a stub |
| `tool.call.completed` | `result` is `msg.ToolResult` (`[]msg.Part`), also carrying `msg_v` |
| `usage.recorded` | one row per non-zero `Usage` class, per `llm.response` (§5.3) |
| `session.error` | `code` maps to one of the neutral error classes (§9); always carries the Provider `request_id` |

## 8. UI

- **`/model` picker**: models grouped per Provider; Providers with no saved key show their models greyed out with "Add `<Provider>` key" and cannot be selected. Token limits are the primary number shown; dollar figures show only as "≈ $" (approximate), and a model with unknown price shows only its token limit, no dollar figure.
- **Provider Key settings**: saving a key runs the free models-list validation call; a bad key is rejected immediately with "Key rejected".
- **Error surface**, one row per neutral class (§9): a short message plus the one or two buttons listed there (e.g. "Provider account out of credit / limit hit" → Open provider billing / Switch model). `rate_limited` shows nothing but a spinner; `bug` shows "Internal error (ID …)" with Retry/Report.
- **"Show thinking"**: a switch in the chat header, default off, remembered per user across chats. Off: no thinking shown. On: thinking streams live in a collapsed grey block above the answer; old messages show their stored thinking the same way. What appears differs by Provider and model (OpenAI and some Anthropic models give summaries only; non-thinking models show nothing) — this is inherent to the Provider, not a bug.
- **Budget**: shown in tokens as the main number; the dollar figure is labelled "≈" throughout. A model with unknown price shows only the token limit, no dollar cap.
- **Upload stub**: the chat UI never changes; the user still sees their file. The stub exists only in the prompt sent to the model (§5.4).

## 9. Decisions

All decided 2026-09-27.

0. **Official SDKs behind our own adapters** (`anthropic-sdk-go`, `openai-go`, `go-genai`), a richer neutral format (file part, citations, multi-part tool results, `NativePart` escape hatch replayed only to the same Provider, `msg_v`), and an embedded model/price catalog. LiteLLM, OpenRouter and Go multi-provider libraries are rejected: none is compatible with ADR 0002 and decrypted Provider Keys never leaving the Worker (research §11).
1. **`msg_v`**: an integer inside every stored message (amended 2026-10-02: inside `message`, not at payload top level; `Kind` is a short string, so v1's `{"type":…}` parts upcast to v2's `{"k":…}`) (`llm.response`, `user.message`, `tool.call.completed`); upcasters live in `internal/msg`. A new `Part.Kind` is additive and never bumps `msg_v`; a shape change to an existing `Kind` does. A Worker seeing an unknown `msg_v` or `Kind` releases the Lease (event-log.md §5.19's rule). ADR 0002 states the same.
2. **JSON keys stay short**, per §3.1's legend: `k`, `text`, `cit`, `blob`, `tu`, `tr`, `th`, `nat`.
3. **Tool results are a list of parts.** `ToolResultPart{CallID, Parts []Part, IsError}`; `tool.Result` content is `[]msg.Part`. A structured JSON result is a text part; the Gemini adapter wraps it as `{"output": …}` / `{"error": …}`.
4. **Strict tool schemas**: off for Connector (MCP) tools, opt-in-on for built-in tools written in the intersection dialect. Args are always validated in Go regardless. The schema-narrowing pass is cached per `tools_hash`. Gemini uses `parametersJsonSchema`.
5. **OpenAI: Responses API only.** `store:false`, full prompt resent every turn, no `previous_response_id`. No Chat Completions adapter now; a separate adapter later only if OpenAI-compatible third parties (Ollama, GLM) are added.
6. **Provider server tools are not used.** Our own Platform Service web search stays the only search tool. `NativePart` keeps the door open without modelling them. *Amended by 6a.*

6a. **Base version uses each Provider's built-in web search** (2026-09-27; ADR 0006 amendment). Our platform search is deferred.
    - Enabled when the Agent's tools include `web_search` (General: yes). The adapter adds Anthropic's `web_search` server tool, OpenAI Responses' `web_search` tool, or Gemini's `google_search` tool. Whether Gemini allows it together with function tools on each model: check at build time and hide it where it doesn't.
    - The search runs inside the Provider during the LLM call: no `tool.call.*` events, no Approval, no Jev (read-only).
    - Search blocks come back as `NativePart` inside `llm.response`, replayed only to the same Provider. After a `/model` switch to another Provider they are projected to plain text with the cited URLs.
    - Billed to the user's key: `Usage.ServerTools` → `usage.recorded{kind: llm, unit: web_search_requests}`, ≈ $ from the catalog (usage-metering.md). No Quota.
    - The chat shows a search card built from the citations.
    - **Web fetch works the same way** (2026-09-27): Anthropic's web fetch server tool, Gemini's URL context tool; OpenAI's `web_search` opens pages itself. Confirm each at build time. Our own `web_fetch` (with SSRF protection) is deferred with our own search.
7. **Stream idle watchdog: 300 s** for all three Providers, resetting on any received event including Anthropic `ping`. Firing cancels the stream with `ErrStreamIdle`, classified `provider_down` (retryable). No overall request timeout on streams. Tune the 300 s later with evals.
8. **`Delta` gets a `Kind`** (0 = text, zero value keeps current callers correct), plus `thinking`, `tool_start`, `tool_args`, and fields `CallID`, `Name`. No usage deltas. Signature/opaque bytes are never emitted as deltas. `Idx` is the neutral part index.
9. **Neutral error classes** — one global list; every adapter maps its own codes onto it; each class carries an action and a UI button:

   | Class | Example | We do | User sees → button |
   |---|---|---|---|
   | `key_invalid` | 401/403 | stop | "Key rejected" → Update key |
   | `billing` | 402, spend limit, no credit (incl. Anthropic 400 spend-limit, spend-cap 429 without retry-after) | stop | "Provider account out of credit / limit hit" → Open provider billing, Switch model |
   | `model_unavailable` | 404, model not allowed for this key | stop | "This key can't use X" → Switch model |
   | `rate_limited` | 429 + short retry-after (≤60 s) | quick in-process retry | nothing (spinner) |
   | `long_wait` | 429 + long wait (>60 s) | `timer.set` sleep, retry later | "Provider busy, retrying at 3:05" → Retry now |
   | `provider_down` | 500/529/504, stream drop, idle timeout | quick retries, then 1/5/15 min backoff (existing layering) | "Provider having trouble, retrying" → Retry now |
   | `too_large` | 413, file too big | stop | "File too large" → Remove file |
   | `bug` | our malformed request (Anthropic 400 markers e.g. `block_binding`, `thinking`, `tool_choice`) | stop, log request ID | "Internal error (ID …)" → Retry, Report |

   Unknown codes: 5xx → `provider_down`, otherwise → `bug`. The request id is always captured and put on `session.error`.
10. **Anthropic `model_context_window_exceeded` is a stop reason**, not an error: mapped to `StopReason=context_exceeded`, and the loop handles it exactly like the window case (Compaction Tier 2, retry once). OpenAI's `context_length_exceeded` 400 still routes the same way (existing agent-loop.md behavior). Unknown finish-reason enum values (e.g. a Gemini value not in go-genai's enum) → `other` + log.
11. **Uploads: always inline** (base64 from our blob), no Provider Files API. Projection rule: an Upload stays inline for the next N=3 user messages after it was attached; after that, at the first user-message boundary (or at Compaction if earlier), the projection swaps it for a stub note (§5.4 for the exact wording). Never swap mid tool loop. The Event Log never changes; the swap is a deterministic, replay-exact projection rule. Accepted cost: one cache break at the swap point. New built-in tool `read_upload(id, pages?)` returns text for text files and the image/PDF itself for others; page ranges for large PDFs. N is tunable later with evals. The neutral part always carries our `BlobRef`, never a Provider file id. Because no Files API is used, hard delete needs no Provider-side cleanup. (Amended 2026-09-27, [uploads-artifacts.md](uploads-artifacts.md): inline budget cap of 24 MB total base64 size across inline Uploads, oldest stubbed early when over budget; `read_upload` results count toward the same budget and are stubbed after 3 user messages, oldest first; full detail in that spec.)
12. **One normalized `Usage` struct per adapter response** (`Input` normalized to uncached, non-written input, `CacheRead`, `CacheWrite5m`, `CacheWrite1h`, `Output`, `Reasoning`, `ServerTools`). `exec` writes one `usage.recorded` event per non-zero token class, in the same tx as `llm.response` (fits `UNIQUE(session_id, seq)`, one row per event). Thinking/reasoning tokens are a detail inside output, not billed twice. `cost_micros` comes from the catalog at record time and is never recalculated.
13. **`Models()` stays the static catalog view** (loop limits, Compaction thresholds). New adapter method `ListModels(ctx) ([]ModelInfo, error)`, with the key bound. *Amended 2026-10-02:* the API calls it on key save with the plaintext it already holds and stores the result in `provider_keys.models`; the Worker refreshes it daily. The API never decrypts a stored key. One endpoint `GET /models`: for each Provider key the user saved, reads the stored list, merges with the catalog, returns one list. Models of Providers with no key show greyed out with "Add `<Provider>` key"; they can't be selected.
14. **Key validation on save: a free models-list call only** (catches typos/wrong keys immediately in settings; also fills the picker). No paid probe. Credit problems surface on the first real turn as a `billing` error.
15. **Catalog is a JSON file in the repo** (`internal/provider/catalog/models.json`, `go:embed`), reviewed like code; per model: limits, capability flags (incl. forced `tool_choice` allowed, thinking mode), prices. No `source_url` field. Live limits are merged at Worker start and daily for Anthropic only (Gemini and OpenAI: `models.json` is the source); prefer live limits, log the diff. The UI shows tokens as the main number; dollars only as approximate "≈ $". An unknown model id is allowed, with conservative default limits and "price unknown".
16. **Budget keeps its dollar limit**, labelled "≈"; for a model with unknown price, only the token limit applies.
17. **Thinking: stored and replayed per existing rules** (unchanged). Displayed only behind a per-user "Show thinking" preference: a switch in the chat header, default off, remembered across chats; when on, thinking streams live in a collapsed grey block above the answer, and old messages show their stored thinking. Pure UI: we always request readable/summarized thinking from the Provider; the switch never changes the request (cache/replay unaffected, no extra cost). What users see differs by Provider (OpenAI and some Anthropic models give summaries only; non-thinking models show nothing). No effort setting is ever sent. *Amended 2026-10-02:* newer Anthropic models default `display` to `"omitted"` (empty thinking text), so the Anthropic adapter sends `thinking:{type:"adaptive", display:"summarized"}` on catalog `adaptive_only` models (same as the default apart from display; byte-stable, so no cache break) and nothing on extended-only models such as Haiku 4.5, which then don't think.
18. **Internal fixed-shape answers** (Tidy memory, Approver LLM check): a neutral `Request.ResponseSchema json.RawMessage`, mapped per adapter to the Provider's structured-output option; no forced tool use. Always validated in Go; invalid → retry once (treated as `malformed_call`). Internal schemas are authored in the intersection dialect.
19. **Build order**: (1) `msg` types + `msg_v` + upcaster skeleton, (2) Anthropic adapter, (3) OpenAI Responses, (4) Gemini, (5) catalog + price JSON, (6) `read_upload` + the swap rule.
20. **Unconfirmed Provider error codes**: no pre-launch live testing. Adapters classify by status code + basic message-pattern (regex) matching; a wrong guess just fails the chat with the fallback class. Accepted.
21. **Second invalid `ResponseSchema` answer** (after the one retry): the Approver falls back to asking the user (the existing Approver-failure rule); Tidy skips this run, logs it, and retries on its next run.
22. **Deleted Uploads**: deleting writes `upload.deleted{upload_id}` and removes the blob. The chat chip shows "(deleted)"; the projection swaps in a deleted stub at once; `read_upload` returns an error result. Message chips are preview-only; delete lives in the session Files panel (Uploads tab); project files are managed in project settings.
23. **Project files**: the prompt carries only an index of project files (name, id, size); the model reads them with `read_upload`. Changes to project files break the cache once for running sessions; accepted (context.md §5.2).

## 10. Edge cases

- **Unknown `msg_v` or unknown `Part.Kind` on read**: the Worker releases the Lease immediately, without running `Decide` (event-log.md §5.19); a compatible Worker picks the session up on the next claim.
- **Unknown Provider finish-reason / error code**: an unmapped finish reason → `StopReason=other` + log; an unmapped error code → `provider_down` if it's a 5xx, otherwise `bug`.
- **Gemini `functionCall` id**: Gemini fills it (live, gemini-3.5-flash-lite: `call_136041`). The adapter keeps it in `ToolUse.Opaque` and echoes it as `functionResponse.id`. A call with no id is matched by the order guarantee the projection already provides (results re-sorted into tool_use order), and no `functionResponse.id` is sent. Our own `ToolUse.ID` stays internal.
- **Gemini signature after a text reply**: Gemini puts it on an empty final part, so it is stored as a trailing `Thinking` part with no part after it, and replay drops it. Live, Gemini accepts the replay without it; its docs do not enforce signatures on non-function-call parts.
- **`/model` switch mid-session, foreign Provider history**: all prior `Thinking` and `Native` parts for the old Provider are dropped from the projection; calls written by another Provider go out under our own id, for the call and its result; Gemini gets the dummy signature on those function calls (agent-loop.md Decision 6, unchanged here).
- **Upload swap lands exactly at a Compaction boundary**: Compaction wins — the swap happens there rather than waiting for the Nth user message, per §5.4.
- **Inline budget exceeded before an Upload's N=3 turns are up**: the oldest inline Upload (or oldest stubbed-eligible `read_upload` result) is stubbed early to get back under the 24 MB cap, ahead of the normal swap schedule (§5.4 items 9–10, [uploads-artifacts.md](uploads-artifacts.md)).
- **Agent calls `read_upload` on an Upload that was never swapped** (still inline): still works; it's just a redundant read of content already in the prompt.
- **Model id not in the catalog**: allowed, conservative default limits, "price unknown" in the UI; no Budget dollar cap for that model.
- **A user saves a key that passes the free list call but has no credit**: nothing surfaces until the first real generation call, which then fails as `billing`.
- **Spend-cap 429 with no `retry-after`**: classified `billing`, not `rate_limited` — a spinner would never resolve.
- **Anthropic 400 naming `block_binding`/`thinking`/`tool_choice`**: classified `bug` (our projection's fault), never `key_invalid` — the UI must not tell the user to fix their key for our own mistake.
- **Idle stream during long silent thinking**: Anthropic's periodic `ping` keeps resetting the watchdog even with no content; OpenAI and Gemini heartbeats are not documented, so a long silent think on them may hit the 300 s watchdog (tune with evals).
- **Structured output (`ResponseSchema`) returns invalid JSON**: one retry as `malformed_call`; after a second failure the Approver falls back to asking the user, and Tidy skips this run, logs it, and retries on its next run (Decision 21).
- **`tool_choice: required`/`one` requested against a model with `forced_tool_choice=false`**: rejected locally by the adapter before the request is sent, not surfaced as a Provider error.

## 11. Acceptance criteria

- A stored `llm.response` payload from any adapter includes `msg_v`; an upcaster fixture for an older `msg_v` produces a byte-identical current-shape message.
- Adding a new `Part.Kind` in a test fixture does not change `msg_v` on unrelated payloads; changing an existing `Kind`'s shape does.
- A tool result fixture with a text part and an image part round-trips through each adapter without flattening (`ToolResultPart.Parts` has both).
- A Connector tool schema using `pattern`/`minLength` is sent non-strict to every Provider; a built-in tool written in the intersection dialect is sent `strict:true` where the Provider supports it, and Gemini receives it via `parametersJsonSchema`.
- A scripted transport that stops sending bytes for 300 s (any of the three fake adapters) fires `ErrStreamIdle`, classified `provider_down`, before any overall-timeout mechanism would (there is none).
- Each error fixture in §9 Decision 9's table maps to the stated class exactly, including the spend-cap-429-without-retry-after case (→ `billing`, not `rate_limited`) and the Anthropic prefix-binding 400 (→ `bug`, not `key_invalid`).
- An Anthropic `model_context_window_exceeded` response fixture produces `StopReason=context_exceeded` and is not routed through the error path at all.
- A fake adapter response with `Usage{Input:100, CacheRead:20, Output:50}` produces exactly three `usage.recorded` rows in the same tx as `llm.response` (no row for the zero `CacheWrite` classes).
- A session hard-deleted after uploading a file leaves no orphaned Provider-side file (none was ever created).
- An Upload attached at user message 1: still fully inline in the projected prompt through user message 4; from user message 5 (or an intervening Compaction, whichever comes first) it appears only as the stub note; the Event Log's stored `user.message` event is unchanged before and after the swap.
- `read_upload("up_7")` after the swap returns the original content as tool-result parts.
- `GET /models` for a user with only an Anthropic key returns Anthropic's live-merged list normally, and OpenAI/Gemini models greyed out with "Add `<Provider>` key"; none of the greyed-out models are selectable in the picker.
- Saving an invalid Provider Key fails immediately from the free models-list call, with no generation call ever made.
- Toggling "Show thinking" does not change the bytes of the request sent to the Provider (request golden is identical on/off); it only changes what the UI renders from the same stored `Thinking` parts.
- A round-trip property test (`msg → wire → msg`, same Provider) is lossless byte-for-byte for `Opaque` fields across all three adapters.
- Cassette fixtures never contain `x-api-key`, `authorization`, or `x-goog-api-key` header values.

## 12. Open gaps

- **Whether OpenAI's `function_call_output` has any error flag.** If not, the adapter prefixes text (e.g. `"Error: …"`) to signal `IsError`; unconfirmed against OpenAI docs.
- **Gemini `candidatesTokenCount` vs `thoughtsTokenCount` overlap.** Settled live: no overlap; `thoughtsTokenCount` is separate and added to `Output` (§5.3).
- **OpenAI `cache_write_tokens` pricing semantics** are undocumented; the catalog's OpenAI cache-write price is a placeholder until confirmed.
- **Exhaustive structured-output and strict-schema model-support lists for OpenAI and Gemini** (Anthropic's list is documented; the other two are not) — needed to set the catalog's `structured_outputs` capability flag accurately per model.
- **Uploads and Artifacts feature** (upload flow, limits, file types, Files panel, `save_artifact`): spec'd in [uploads-artifacts.md](uploads-artifacts.md). Decisions 22–23 here only cover the prompt/tool side.
- **Long-context pricing tiers** (which current models have a >200k-token price step) are not enumerated; the catalog needs this filled in per model before cost figures for those models are trusted.
- Everything else raised in the research doc's "Open questions for grilling" (Q1–Q13) was settled by the decisions in §9 above.

## 13. Research

Background, prior-art survey (Vercel AI SDK, LiteLLM, Pydantic AI, LangChain v1, OpenAI Agents SDK, Gemini CLI, Codex CLI), per-Provider pain points, streaming shapes, usage/cost, model catalog, keys/errors, uploads, structured output, build-vs-adopt, and full source list: [../research/provider-gateway.md](../research/provider-gateway.md).
