# Streaming (SSE): Research

Status: research, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): **Session**, **Event Log**, **Worker**, **Lease**, **Steering**, **Interrupt**, **Child Session**, **Channel**, **Sandbox**. Ground truth: [event-log.md](../design/event-log.md) §5.11–5.12 (DeltaBus over `pg_notify` with 50 ms coalescing, <8 KB payload limit; SSE live + replay with `Last-Event-ID` header, seq as SSE `id`, delta frames carry NO id, per-child-session streams — Decisions 6, 9, 16), [agent-loop.md](../design/agent-loop.md) Decision 14 (callback streaming interface `Stream(ctx, req, onDelta)`), [context.md](../design/context.md) (compaction interaction with streaming), [uploads-artifacts.md](../design/uploads-artifacts.md) (blob_ref/preview origin for large payloads over streaming), [product.md](../product.md) (chat streamed over SSE with `Last-Event-ID` resume), [approver.md](../design/approver.md) (Jev shown as "auto mode"), [agents-skills.md](../design/agents-skills.md) §8 (child cards with Stop). SSE auth: decided in Auth and keys. The Sandbox is not in the base version, so `sandbox.*` events never appear on the stream there.

Reviewed 2026-09-27: Cloudflare, iOS, CVE and prior-art resume claims checked against sources; wrong ones struck or fixed; questions reframed (§10).

---

## 1. Summary

- **SSE is the decided transport for live + replay streaming** (§5.12): unidirectional HTTP, with native browser `EventSource` API sending `Last-Event-ID` on reconnect (WHATWG HTML spec, MDN EventSource docs). Delta frames (partial text/thinking/tool args) carry no `id` and are ephemeral; only final durable events (like `llm.response`) carry a seq-as-id, so reconnect always restarts from the last durable state. Deltas live only in memory, coalesced over DeltaBus (`pg_notify`).
- **Native `EventSource` cannot set custom headers**; custom-header auth (Bearer tokens, API keys) requires `@microsoft/fetch-event-source` or equivalent fetch-based polyfill (GitHub Azure/fetch-event-source README). URL-based tokens risk leakage through logs, history, referrers; cookies are the safer channel if CSRF is defended against.
- **Proxy buffering breaks SSE** (nginx docs, Cloudflare reports): must set `proxy_buffering off` on the backend or send `X-Accel-Buffering: no` header (nginx honors it as a response-level override). Cloudflare (Workers, Tunnels) has open reports of SSE being held until the response ends; causes vary and fixes are vendor-specific (§3).
- **Go SSE servers need `http.Flusher`** (pkg.go.dev) on every write to push data immediately; `http.ResponseController` (Go 1.20+) enables per-write deadlines for backpressure. LISTEN/NOTIFY requires a dedicated non-pooled connection per server process (jackc/pgx docs, brandur.org) — one connection listens in-process and fans out to all local clients; no sharing with regular query pool.
- **Mobile and multi-tab scenarios** (MDN Page Visibility API, Apple developer forums): an iOS PWA that is backgrounded for ~20 s loses its SSE connection; on iOS 18 the `error` event does not fire on return and `readyState` still says OPEN (a zombie). So the app must close on `visibilitychange: hidden` and reopen on `visible` with `Last-Event-ID`. Sharing one SSE connection across browser tabs (BroadcastChannel + leader election) reduces server load.
- **Connection limit**: over HTTP/1.1 a browser allows 6 connections per origin, shared by all tabs; each open `EventSource` holds one. HTTP/2 lifts this (~100 streams), but browsers only speak HTTP/2 over TLS, so plain `http://localhost` dev is HTTP/1.1. This is the main shape question (Q0): one stream per Session vs one per-user stream.
- **Prior art shows two patterns**: (a) fine-grained deltas streamed as semantic events (Anthropic `content_block_delta`, OpenAI Responses API `deltas` — each Provider has its own typing/naming); Vercel AI SDK and LangGraph expose transformers to project raw events into per-channel streams. (b) Coarser events with optional previews: event-log.md Decision 9 says deltas are ephemeral and `<8 KB`; only durable events carry seq. Liveblocks Sync / presence systems send incremental updates with eventual-consistency semantics — faster than strict seq-ordered durability but require conflict resolution.
- **`Stream(ctx, req, onDelta)` callback contract** (agent-loop.md Decision 14): the Provider calls `onDelta(Delta)` for each partial update and returns the final `Response` on completion. Never persist deltas; never count them toward token usage (only the final response does). The callback is easier to write and test than an iterator/channel, and simpler under cancellation.
- **What goes on the stream**: Decision 9 says token deltas are ephemeral; the log holds only `llm.response`. Ephemeral deltas (`kind: text | thinking | tool_start | tool_args`) never get `seq` and never get retried on reconnect. Durable events (all others) get `seq`, are queryable, and are replayable. For large payloads over 32 KB (event-log.md §3, uploads-artifacts.md), use `blob_ref + preview` so preview appears inline and full content is fetched on demand via a tool or link, not streamed inline (cross-spec with uploads-artifacts Decision 11: inline base64 only — previews defer the blob).
- **Child session streams** (event-log.md §5.12) are per-child; the parent's stream shows `child.*` events (e.g. `child.started`, `child.completed`), and opening a child card in the UI opens a separate stream to `GET /sessions/{child_id}/events` with its own `Last-Event-ID` tracking.

---

## 2. SSE protocol facts

### Core fields and semantics

Per the [WHATWG HTML living standard (server-sent events section)](https://html.spec.whatwg.org/multipage/server-sent-events.html):

- **`id` field**: sets the event source's last event ID string. Client sends this back in the `Last-Event-ID` request header on reconnect. Reconnection requires the `id` to persist uniquely per session — in jelly-fish, `seq` serves this role (event-log.md Decision 6).
- **`retry` field**: integer in milliseconds. If set by the server, the client respects it (e.g., `retry: 2000` means "wait 2 seconds before reconnecting"). Browser EventSource defaults to ~3 seconds on network close, so the server can override with `retry: <value>`.
- **`Last-Event-ID` request header**: sent by the client on reconnect after a drop. Server parses this to know which events to replay from. WHATWG spec says the server may ignore it or use it for replay (event-log.md §5.12 uses it to resume from the last seq).
- **Comment lines** (`:` at the start): treated as comments and ignored by the client. Legacy systems recommend a comment every 15 seconds as a heartbeat to keep proxies from dropping idle connections ([WHATWG spec](https://html.spec.whatwg.org/multipage/server-sent-events.html) explicitly advises this for proxy robustness).
- **Named event types** (`event: <name>` field): sends events with type names other than the default "message". Client calls `addEventListener('<name>', handler)` to listen for specific types. Event-log.md §5.12 uses `event: delta` for streaming updates.

### Browser EventSource API limits

[MDN EventSource docs](https://developer.mozilla.org/en-US/docs/Web/API/EventSource):

- **Six-connections-per-origin limit (HTTP/1.1 only)**: the browser limits concurrent SSE connections to 6 per domain per browser instance. This is [marked "Won't fix" in Chrome](https://crbug.com/275955) and [Firefox](https://bugzil.la/906896).
- **HTTP/2 mitigation**: HTTP/2 negotiates a concurrent streams limit (default ~100); this effectively removes the 6-connection bind.
- **No custom headers**: `EventSource` constructor takes only `url` and `withCredentials` parameters. Cannot set `Authorization: Bearer`, custom `Content-Type`, or any other header. This is the key limitation driving developers to fetch-based clients.
- **GET only**: `EventSource` always uses GET, so request bodies (if needed for auth or complex filtering) are impossible.

### Fetch-based SSE clients

[`@microsoft/fetch-event-source`](https://github.com/Azure/fetch-event-source) (GitHub README) exists because of native `EventSource` constraints:

- **Custom headers**: `Authorization: Bearer <token>` and any other header can be sent.
- **Any HTTP method**: POST, PUT, DELETE allowed (not just GET).
- **Request body**: complex filters or metadata can be POSTed instead of URL-encoded.
- **Retry control**: developer controls retry strategy; can honor `retry` field or use custom backoff.
- **Page Visibility API integration**: fetch-event-source can close the connection on `visibilitychange: hidden` and reconnect on `visible`, with `Last-Event-ID` resumption ([fetch-event-source README](https://github.com/Azure/fetch-event-source)).

**Common reason to use fetch-event-source**: token authentication. "Your API expects `Authorization: Bearer ...`, and the browser's `EventSource` constructor has nowhere to put that header" ([Milan Jovanović](https://milanjovanovic.tech/blog/fetch-event-source)).

---

## 3. Proxies and infrastructure that break SSE

### Nginx buffering

[nginx docs (ngx_http_proxy_module)](https://nginx.org/en/docs/http/ngx_http_proxy_module.html) and [nginx conventions](https://github.com/nginx/documentation) describe response buffering:

- **Default**: `proxy_buffering on` (enabled). Nginx buffers the proxied response until the buffer fills (~16 KB default) before sending it to the client. This *batches* SSE events, breaking real-time.
- **Solution**: set `proxy_buffering off` in the upstream block for SSE routes. This disables buffering immediately, sending each write to the client.
- **`X-Accel-Buffering` header**: the application can send this response header; nginx honors it and disables buffering for that response only. Format: `X-Accel-Buffering: no`. The header is removed before reaching the client. Non-nginx proxies typically ignore it.

### Cloudflare buffering

Verified 2026-09-27 (the earlier "~100 KB" figure was not found in the cited sources; struck):

- **Workers**: [mastra-ai/mastra#13584](https://github.com/mastra-ai/mastra/issues/13584): on Workers the whole ~10 KB response arrived in one burst after ~10 s. Cause: Hono's `stream()` helper wrapped the Response through a `TransformStream`; returning `new Response(ReadableStream)` directly fixed it. An app-code bug, not a platform limit.
- **Tunnels**: [cloudflare/cloudflared#1449](https://github.com/cloudflare/cloudflared/issues/1449) (open): SSE over GET through a Quick Tunnel is flushed only when the server closes the connection. [cloudflared#199](https://github.com/cloudflare/cloudflared/issues/199) is an older report of the same.
- **Takeaway for jelly-fish**: we don't run on Workers. If a friend-facing deploy sits behind a Cloudflare proxy or tunnel, add a smoke test that sees a delta arrive before the turn ends. Not a design input.

### Compression and idle timeouts

General proxy issues (not vendor-specific):

- **Compression interaction**: gzip compression of SSE responses can cause buffering if the compressor waits for enough data to compress efficiently. Disable compression for streaming endpoints (`Content-Encoding: identity` or configure the proxy).
- **Idle timeouts**: proxies may close a connection if no bytes are sent for N seconds. Heartbeat comments (`:` lines) every 15 seconds prevent this ([WHATWG spec](https://html.spec.whatwg.org/multipage/server-sent-events.html), [Cloudflare docs](https://www.server-sent-events.com/sse-protocol-fundamentals-architecture/proxy-and-cdn-configuration-for-sse/)).

Already decided: event-log.md §5.12 sends a `: ping` comment every 15 s. That covers the idle-timeout case.

---

## 4. Go implementation details

### http.Flusher and streaming

[`http.Flusher`](https://pkg.go.dev/net/http#Flusher) (pkg.go.dev) is an interface with one method:

```go
type Flusher interface {
    Flush()
}
```

Its purpose: flush any buffered data to the client immediately, without waiting for the response to close. When you write to `http.ResponseWriter`, data may be buffered; calling `Flush()` forces it across the network.

**Necessity**: for SSE, after each event is written (`fmt.Fprintf(w, "data: %s\n\n", ...)`) and before the next, call `Flush()` to push that event to the client. Otherwise, kernel or application buffers hold the data until the response completes or a buffer boundary is crossed.

**Implementation**:

```go
if f, ok := w.(http.Flusher); ok {
    f.Flush()
}
```

Type-assert before calling, since not all `ResponseWriter` implementations support flushing.

### http.ResponseController and per-write deadlines

[`http.ResponseController`](https://pkg.go.dev/net/http#ResponseController) (added Go 1.20) wraps a `ResponseWriter` and provides per-operation deadline control:

```go
rc := http.NewResponseController(w)
rc.SetWriteDeadline(time.Now().Add(5 * time.Second))
// now write; will timeout after 5s
rc.Flush()
```

**Use case for SSE**: detect slow clients. If a client isn't consuming events (e.g., the browser tab is suspended), writes will block. A per-write deadline allows the server to detect this and close the connection gracefully instead of holding resources indefinitely.

**Backpressure handling**: pair with a buffered channel (size N) or drop policy. If the client is slow, the channel fills; then either block the goroutine (risk of resource exhaustion), drop new events, or close the connection. Event-log.md Decision 9 doesn't specify this, so it's an open implementation question (Q1 below).

### pgx LISTEN/NOTIFY and connection management

[jackc/pgx documentation](https://github.com/jackc/pgx) and [brandur.org: The Notifier Pattern](https://brandur.org/notifier):

LISTEN is a session-scoped subscription to a Postgres notification channel. The subscription lives on a single connection and dies when the connection closes. Because most applications use a connection pool (pgxpool), recycling a pooled connection would lose the subscription.

**Architecture**:

- **One dedicated LISTEN connection per application process**, held open for the lifetime of the process. This connection must NOT be recycled into the pool.
- **Fan-out in-process**: when a `NOTIFY` arrives, the Worker process receives it on the LISTEN connection and pushes it to all locally-connected clients via an in-process hub (event-log.md §5.12 mentions "fed by LISTEN jf_events + DeltaBus on the API node").
- **Multiple Workers**: each Worker instance holds its own LISTEN connection and receives every NOTIFY independently. Events are not deduplicated or routed by instance; all instances receive all NOTIFYs.
- **Regular queries**: all other database calls use the normal pgxpool with transaction or statement pooling enabled.

[**Constraint**](https://github.com/jackc/pgx/issues/1121): a single LISTEN connection cannot multiplex `LISTEN` subscriptions and regular queries in the same pool. Mixing causes subscription loss on pool recycle.

⚠ **cross-spec**: event-log.md §5.12 assumes one dedicated LISTEN connection per Worker ("a dedicated non-pooled conn; on reconnect: re-LISTEN, then poll"). This decision is sound; it's confirmed by pgx best practice.

---

## 5. PWA and mobile behavior

### Page Visibility API

[MDN Page Visibility API docs](https://developer.mozilla.org/en-US/docs/Web/API/Page_Visibility_API):

- **`document.hidden`**: boolean; `true` if the page is not visible to the user.
- **`document.visibilityState`**: string; "visible" (foreground tab of a non-minimized window) or "hidden" (background tab, minimized window, screen off).
- **`visibilitychange` event**: fires when visibility state changes. No payload; read `document.visibilityState` to determine the new state.

**Use for SSE**: on `visibilitychange`, listen for the state change. If `hidden`, close the SSE connection (to free server resources and avoid accumulating a zombie connection). If `visible`, reconnect with `Last-Event-ID` set to the last received seq, so the server replays missed events.

[**Example** from fetch-event-source](https://github.com/Azure/fetch-event-source): the library integrates with Page Visibility API so that connections are closed when the page is hidden and automatically retry with `Last-Event-ID` when it becomes visible again.

### iOS Safari behavior

Verified 2026-09-27 against [Apple developer forums thread 765183](https://developer.apple.com/forums/thread/765183) ("EventSource: event 'error' not fired in iOS18"):

- **Backgrounding**: a PWA left in the background for ≈20 s has its SSE connection closed.
- **iOS 17**: on return to the foreground, `error` fires, so the app could reconnect from that.
- **iOS 18 regression**: `error` does not fire on return, and `readyState` is still 1 (OPEN) although the connection is gone. The app sits on a dead stream.
- **Workaround**: close the connection on `visibilitychange: hidden` and reopen on `visible` with `Last-Event-ID` (what fetch-event-source does by default).

Struck: "suspended within ~5 s in low-power mode since iOS 13" and "25 s heartbeat is safer". They come from an unsourced guide (server-sent-events.com), and the MagicBell post doesn't mention SSE at all. The 15 s ping is decided (event-log.md §5.12) and doesn't help here anyway: a suspended page can't receive pings.

---

## 6. Prior art: resumable streaming and multi-tab viewing

### OpenAI Responses API

[OpenAI Responses API docs](https://developers.openai.com/api/docs/guides/streaming-responses) and [community guide](https://community.openai.com/t/responses-api-streaming-the-simple-guide-to-events/1363122):

- **Streaming format**: SSE with semantic event types named `response.*` (e.g. `response.output_text.delta`, `response.function_call_arguments.delta`, `response.completed`). Every event carries a `sequence_number`. (The earlier `content_block_*` names here were Anthropic's; corrected.)
- **Resume (verified 2026-09-27, [background mode guide](https://developers.openai.com/api/docs/guides/background))**: only for `background: true` responses. The client keeps the last `sequence_number` as a cursor and reconnects with `GET /v1/responses/{id}?stream=true&starting_after=42`. Same idea as our `Last-Event-ID` / `?after=`, but query-param based, not the SSE `id` header.

### Anthropic Messages API

[Claude Platform Docs: Streaming messages](https://platform.claude.com/docs/en/build-with-claude/streaming) and [DEV Community: Streaming Tool Calls](https://dev.to/gabrielanhaia/streaming-tool-calls-parse-anthropic-sse-without-loading-the-whole-message-2on):

- **Streaming format**: SSE with typed event envelope (`content_block_start`, `content_block_delta`, `content_block_stop`).
- **Deltas**: `content_block_delta` carries `type: text_delta` (text), `type: input_json_delta` (tool args, as JSON string fragments), or others. Each delta has an `index` (content block position in the final message).
- **Thinking blocks**: streamed as a separate content block type; deltas interleave with text and tool-use deltas on their respective indices.
- **Resume (verified 2026-09-27)**: no SSE-level resume (no event ids, no `Last-Event-ID`). The docs' "Error recovery" section says: for Claude 4.5 models and earlier, send a new request with the partial assistant response so far and continue from there. Not relevant to our UI stream: the Worker, not the browser, holds the Provider stream (a drop there is `provider_down`, agent-loop.md §5.3).

### Vercel AI SDK (useChat and stream protocol)

[Vercel AI SDK UI: Stream Protocols](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol) and [Vercel AI SDK Streaming Guide](https://vercel.com/docs/functions/streaming-functions):

- **Protocol**: the SDK's "AI SDK Data Stream Protocol" multiplexes tokens, tool calls, tool results, and finish events over a single HTTP response using a custom format (not bare SSE; a layer above HTTP).
- **Text streaming**: text content uses a `start`/`delta`/`end` pattern with unique IDs per block. Each delta carries the substring since the last.
- **Multi-turn and state**: `useChat` React hook manages the chat history and UI state; it handles token streaming on the client and re-renders as deltas arrive. The hook is not tied to SSE; it works with any `streamProtocol`.
- **Resume (verified 2026-09-27, [Chatbot Resume Streams](https://ai-sdk.dev/docs/ai-sdk-ui/chatbot-resume-streams), [vercel/resumable-stream](https://github.com/vercel/resumable-stream))**: `useChat({ resume: true })` calls `GET /api/chat/[id]/stream` on mount; the server returns 204 if no stream is active. The `resumable-stream` package keeps the stream in Redis (one `INCR` + `SUBSCRIBE` per stream); the producer always finishes the generation even if the reader leaves; a reader resumes at an integer position (`resumeAt`). Lesson that matches our design: in a resumable setup, closing the tab or `stop()` is only a disconnect, so a real Stop needs its own endpoint (ours: `user.interrupt` POST).

### LangGraph streaming

[LangGraph docs: Streaming](https://docs.langchain.com/oss/python/langgraph/streaming) and [LangGraph v1.2 event streaming](https://reference.langchain.com/python/langgraph/stream):

- **Multi-mode streaming**: `stream_mode` can be "updates" (latest node's output), "values" (full state snapshot), "messages" (message deltas), "custom" (user-defined), "checkpoints", "tasks", or "debug".
- **Event envelope**: each event carries `channel` (source), `namespace`, `sequence` metadata, `timestamp`, and a typed `payload`.
- **Transformers**: graph can be compiled with `transformers=[...]` to project raw events into per-channel iterators, so consuming code doesn't branch on event types.
- **Resume (verified 2026-09-27, [LangSmith Agent Server streaming](https://docs.langchain.com/langsmith/streaming))**: "Thread streams support resumability via the `Last-Event-ID` header." A thread stream follows all runs on a thread (like our per-Session stream); a single run's stream closes when the run ends. Closest prior art to our §5.12 design.

### Liveblocks Sync and Presence

[Liveblocks blog: Introducing Liveblocks Sync (Feb 2026)](https://liveblocks.io/blog/introducing-liveblocks-sync-the-sync-engine-for-the-agentic-web) and [Liveblocks docs: Storage & Presence](https://liveblocks.io/docs/ready-made-features/multiplayer/sync-engine/liveblocks-storage):

- **Sync engine**: real-time collaboration engine for documents shared across users/agents. Uses WebSocket (not SSE).
- **Presence**: ephemeral updates like cursor positions, user activity. Uses `.setPresence()` method; updates batched with storage updates.
- **Multi-tab/device**: Presence updates propagate between all connected clients in ~50–200 ms (depending on geography), with automatic deduplication on the server.
- **Resume**: the sync engine keeps full document history on the server and sends new clients the full snapshot + incremental deltas from the snapshot point onward. No SSE; event-sequencing is server-managed but at a higher semantic level (OT/CRDT, not raw events).
- **Connection model**: one connection per browser tab (no sharing, or optional sharing via SharedWorker). Each connection reconnects on drop; the server dedupes presence updates per user.

**Takeaway**: Liveblocks is built for collaborative editing (bidirectional, eventual-consistency semantics). Jelly-fish's streaming is unidirectional (Server → UI), durable, and seq-ordered (stronger guarantees). The multi-tab presence pattern is interesting (broadcast new arrivals to all tabs) but not applicable here.

---

## 7. SSE vs WebSocket vs HTTP streaming (NDJSON) as options

Trade-offs, not a decision (Steering and Interrupt are already POST requests per the specs):

| Factor | SSE | WebSocket | HTTP streaming (NDJSON) |
|--------|-----|-----------|-------------------------|
| **Protocol overhead** | HTTP header per connection | WebSocket handshake + binary framing overhead | Per-request HTTP headers; multiplexing via HTTP/2 |
| **Latency (TTFB)** | Low; HTTP/3 avoids extra roundtrips | Slight overhead from upgrade handshake | Low if multiplexed over HTTP/2 |
| **Directionality** | Server → Client only (one-way) | Bidirectional (both directions) | Server → Client in response body; client → server via separate POST requests |
| **Browser support** | Native `EventSource` API; wide support | Native WebSocket API; wide support | No native API; fetch-based polling or long-polling |
| **Reconnection** | Automatic (browser handles it) + `Last-Event-ID` header | Manual (application must implement) | Manual (application must implement) |
| **Infrastructure** | Standard HTTP; works with HTTP/2 and existing proxies | Requires WebSocket upgrade; not all proxies support it | Standard HTTP; works everywhere |
| **Debugging** | Text-based (readable in browser DevTools) | Binary frames (harder to inspect; DevTools show hex) | Text-based JSON (readable) |
| **Library ecosystem** | Lightweight; fetch-event-source for custom headers | Rich libraries (e.g., Socket.IO) | Lightweight; fetch-based |
| **Idle connection handling** | Heartbeat comments needed for aggressive proxies | Keep-alive pings; not always effective | Polling required; resource-intensive |

**Jelly-fish context**: Steering and Interrupt are sent as plain POST requests (event-log.md §5.8, agent-loop.md §5.4), so the stream itself is read-only. SSE is the natural fit: simpler than WebSocket, more reliable on hostile proxies, has native reconnection + `Last-Event-ID`, and the text-based format is easy to debug.

---

## 8. Stream authorization

### Cookies vs bearer tokens

[Bearer Tokens Explained (Security Boulevard, 2026)](https://securityboulevard.com/2026/01/bearer-tokens-explained-complete-guide-to-bearer-token-authentication-security/) and [GitHub issue: Token leakage in SSE URLs](https://github.com/Traitome/oxo-flow/issues/522):

- **Bearer tokens in the `Authorization` header**: require custom headers, which native `EventSource` cannot send. Requires fetch-event-source or equivalent.
- **Bearer tokens in the URL query string** (fallback when headers unavailable): leak through HTTP access logs, browser history, Referer headers, and CDN cache keys. **This is a real, documented risk** ([GitHub issue example](https://github.com/Traitome/oxo-flow/issues/522)).
- **Cookies** (HttpOnly, Secure, SameSite): cannot be set by JavaScript, so they're protected from XSS stealing them. However, they reintroduce CSRF risk for GET requests (SSE endpoints are typically GET).

### CSRF protection for SSE

[OWASP CSRF docs](https://owasp.org/www-community/attacks/csrf) and [CVE-2026-61593: djust SSE CSRF](https://www.strix.ai/cve/CVE-2026-61593):

Verified 2026-09-27: CVE-2026-61593 is real (djust < 1.0.7, a Django reactive-rendering library, CVSS 8.1).

- **What went wrong in djust**: "the SSE client→server POST endpoints are `@csrf_exempt` and the SSE GET stream endpoint had no Origin check." Session ids were client-chosen UUIDs, and POSTs with `text/plain` skipped the CORS preflight. So a hostile page could drive the victim's SSE session.
- **Lesson**: the danger is cookie-authed *state-changing* POSTs without CSRF defence, plus a stream that trusts ids the client picks. A read-only GET stream mostly leaks data only if CORS lets the hostile page read it.
- **Mitigation** (the 1.0.7 fix): check `Origin` against an allowlist on all SSE endpoints (403 otherwise), and require `Content-Type: application/json` on POSTs.
- **For jelly-fish**: our stream is read-only and Steering/Interrupt are separate POSTs. The cookie/CSRF/Origin rules belong in Auth and keys.
- **Best practice**: pair cookie auth with Origin/Referer/Sec-Fetch-Site validation on all HTTP-method boundaries (GET/POST/PUT). For SSE specifically, if the endpoint is cookie-authenticated, validate Origin.

### Token auth with short lifetime and refresh tokens

[Crosscheck: Cookies vs JWT (2026)](https://crosscheck.cloud/blogs/cookies-vs-jwt-authentication-2026/):

Auth0, Clerk, and Okta's 2026 pattern:

- **Access token**: short-lived (60 seconds to 15 minutes), can be a Bearer token sent in `Authorization` header (fetch-event-source) or stored in an HttpOnly cookie.
- **Refresh token**: long-lived, stored in an HttpOnly, Secure, SameSite cookie. Used to obtain a new access token when the old one expires.
- **SSE for custom-header auth** (Bearer token): use fetch-event-source + send the access token in `Authorization` header. The token expires eventually; the SSE client detects 401 and calls the refresh endpoint to get a new token, then reconnects.

### Single-use ticket pattern

**Alternative** (from GitHub issue examples): to avoid leaking tokens in URLs and simplify cookie-vs-headers trade-offs:

1. POST `/api/events/ticket` (authenticated with a long-lived token or refresh token) → server issues a short-lived (e.g., 30-second), single-use code.
2. Client opens SSE stream: `GET /sessions/{id}/events?ticket=<code>`.
3. Server validates the ticket on the SSE request handshake and consumes it (one-time use).
4. If the SSE connection drops, the client must POST for a new ticket before reconnecting.

**Pros**: no token in the URL past the initial handshake, and single-use codes are harder to exploit. **Cons**: extra round-trip per SSE connection.

### Tenant scoping

Event-log.md §5.15 (TenantScope): all repository methods take a `TenantScope{WorkspaceID, UserID}` and reject a session in another workspace. On the SSE endpoint, the authorization check must verify that the authenticated user owns or is a member of the workspace/project that contains the session.

⚠ **cross-spec**: **the SSE auth mechanism is decided in Auth and keys, not here.** Streaming only records the dependency. Native `EventSource` can't send headers, so cookies are near-mandatory (the Auth research says the same). Tenant scoping is enforced at the repository/query layer (event-log.md Decision 12: app-level tenant scoping, RLS later).

---

## 9. What goes on the stream: durable vs ephemeral, filtering, and multi-tab patterns

### Durable events vs ephemeral deltas

Event-log.md Decisions 6, 9:

- **Durable events**: everything in the event log (user messages, tool calls, approvals, etc.). Each carries a unique `seq` per session (gapless). On SSE, durable events are sent with `id: <seq>`, so the client can track the last received seq and reconnect with `Last-Event-ID: <seq>`. The server replays all events with `seq > <seq>`.
- **Ephemeral deltas**: token text, thinking blocks, partial tool arguments from the streaming Provider response. These arrive as `event: delta` (no `id`) and are never stored in the log. They are coalesced by DeltaBus over `pg_notify` (50 ms window, <8 KB payload). On reconnect, deltas are lost; the client waits for the next durable event (`llm.response`) to resume.

**Implication**: if the SSE connection drops mid-turn (while the Provider is streaming), the client drops any partial text buffered for that turn and waits for the final `llm.response` event to be replayed. The text deltas are gone, but the user sees the final response when they reconnect (plus the delta stream resumes in real-time if the turn is still ongoing).

### Filtering and visibility

- **System Sessions** (event-log.md §1): sessions started by the platform for background jobs (e.g., Tidy memory). The Event Log stores them like any session, but they should *not* appear in user-facing stream endpoints (e.g., no session list/inbox stream showing all sessions). The filter is at the API / repository layer (TenantScope + is_system check). An implementation detail, not a grilling question (former Q3 merged here).
- **Internal fields**: the Event Log payload carries internal metadata (e.g., approver traces, usage breakdowns). UI-facing payloads should filter these out at serialization time (or jelly-fish's ADR 0002 neutral message format already does this via projection; not restated here).
- **"Jev" naming**: CONTEXT.md says Jev is an internal name; users see "auto mode" in the UI. The SSE stream must never leak "Jev" into the payload sent to the client; if the event-log payload carries `approver_model: "jev"`, the serializer replaces it with `approver_model: "auto"` or omits it before sending to the client.

### Large payloads over the stream: blob_ref and previews

Event-log.md §3 and uploads-artifacts.md Decision 11:

- **Inline limit**: inline payload parts (text, images, etc.) must not exceed ~32 KB total per event (to keep SSE events small and fast to transmit).
- **Large payloads**: tool results, screenshots, or file reads that exceed 32 KB are stored as blobs and replaced inline by a `blob_ref + preview` object:

```json
{
  "blob_ref": {
    "key": "ws/{workspace_id}/sess/{session_id}/blobs/{sha256}",
    "size": 81234,
    "sha256": "9f2c…",
    "mime": "text/plain"
  },
  "preview": "<first 2 KB of content>"
}
```

- **Preview origin** (uploads-artifacts.md §5): previews are served from a separate, cookie-less origin (e.g., `preview.jellyfish.local`) to prevent XSS. The full blob is fetched on-demand via a tool (e.g., `read_upload`) or a presigned URL, not streamed inline.
- **SSE + preview**: the `preview` field (first 2 KB) appears inline in the SSE event, so the UI can show a preview immediately. If the user wants the full content, they call a tool or follow a link to fetch it.

### Child session streams

Event-log.md §5.12:

- **Parent stream**: shows `child.started` and `child.completed` events on the parent session's stream. The event carries the child session ID.
- **Child stream**: a separate stream endpoint `GET /sessions/{child_session_id}/events`, with its own `Last-Event-ID` tracking. Opening a child card in the UI subscribes to the child stream separately.
- **No cross-session event replay**: events from the child stream are not mixed into the parent stream. The UI maintains separate streams and buffers for parent and child.

### Multi-tab and multi-device viewing of the same session

**Not yet designed** (not in the specs). Open questions:

- Should SSE deltas be deduplicated or shared across tabs viewing the same session? (This is a client-side optimization, not a server-side streaming feature, so likely out of scope here.)
- Should the server track which tabs/devices are connected and send deltas only once? (Likely no; the server cannot reliably track client state and shouldn't need to.)
- Should opening the same session in a second tab reconnect to the first tab's stream, or open a new SSE connection? (Likely new connections; see Q7 below.)

**Prior art** (from §6): Liveblocks and fetch-event-source document using BroadcastChannel API or SharedWorker to share one SSE connection across tabs (leader election; one tab holds the connection and broadcasts events to others). This is a client-side optimization, not a server design concern.

---

## 10. Open questions for grilling

Reviewed 2026-09-27: Q0 reframed; Q2 (heartbeat) and Q6 (`/model`) dropped because event-log.md §5.12 and Decision 35 already decide them; Q3 merged into §9 as an implementation detail; Q8 moved to Auth and keys.

**Q0: One stream per Session, or one per-user stream?** Today (§5.12) each open chat holds one `GET /sessions/{id}/events`, and each expanded child card opens another (agents-skills.md §8, up to 5 running children). The session list also needs live status badges (running / needs approval / done) for sessions that aren't open. Over HTTP/1.1 the browser allows 6 connections per origin, shared across all tabs, so parent + 5 child cards + a second tab already hits the limit and the next request hangs. HTTP/2 removes the limit but needs TLS (browsers don't speak h2c), so `http://localhost` dev is HTTP/1.1. Options: (a) keep per-Session streams, add one per-user "activity" stream for badges, require HTTP/2 in deploys and collapse child cards to one open at a time in dev; (b) one per-user multiplexed stream that carries every subscribed Session's events and deltas, with subscribe/unsubscribe POSTs; (c) per-Session streams only, and the list polls.

**Touches**: event-log.md §5.12 (per-child streams, `Last-Event-ID` is per Session), agents-skills.md §8 (child cards), product.md (PWA).

---

**Q1: Backpressure and slow-client handling.** If a client is not consuming events fast enough, should the server (a) block, (b) drop deltas, or (c) close the connection so the client reconnects? The DeltaBus hub must never block the LISTEN reader on one slow client.

**Proposed answer**: bounded per-client channel; if it's full, drop deltas for that client; if a write blocks past a `http.ResponseController.SetWriteDeadline()` deadline, close the connection. The client reconnects with `Last-Event-ID` and loses only deltas (already allowed by §5.12).

**Touches**: event-log.md §5.11–5.12, Go implementation.

---

**Q2**: dropped (15 s ping and `retry: 2000` decided in event-log.md §5.12).

**Q3**: merged into §9 (System Sessions filter is an implementation detail).

---

**Q4: Bad `Last-Event-ID`.** Non-numeric, or larger than the Session's `last_seq`?

**Proposed answer**: non-numeric → treat as 0 (full replay). Larger than `last_seq` can only be a client bug (seq never resets), so also full replay, and the client rebuilds its view. Never 400: `EventSource` would just retry forever.

**Touches**: event-log.md §5.12.

---

**Q5: Who closes a child card's stream?**

**Proposed answer**: the server closes the child's stream after sending the child's terminal event (`session.completed` / `failed`), with no `retry`, so the browser doesn't reconnect forever; the client also closes it when the card collapses.

**Touches**: event-log.md §5.12, agents-skills.md §8.

---

**Q6**: dropped (`/model` is `session.config_changed`, event-log.md Decision 35; the stream just carries it).

---

**Q7: Several tabs on the same Session.**

**Proposed answer**: server doesn't care; each tab has its own stream. Tab sharing (BroadcastChannel) only if Q0 keeps per-Session streams and the limit bites.

**Touches**: product.md (PWA).

---

**Q8**: moved to Auth and keys (token refresh / cookie expiry on a long-lived stream).

---

## 11. Sources

- [WHATWG HTML living standard — server-sent events section](https://html.spec.whatwg.org/multipage/server-sent-events.html)
- [MDN EventSource API](https://developer.mozilla.org/en-US/docs/Web/API/EventSource)
- [MDN Page Visibility API](https://developer.mozilla.org/en-US/docs/Web/API/Page_Visibility_API)
- [GitHub Azure/fetch-event-source](https://github.com/Azure/fetch-event-source)
- [Pon Tech Talk (Medium): Extend EventSource API with @microsoft/fetch-event-source](https://medium.com/pon-tech-talk/extend-the-usage-of-the-eventsource-api-with-microsoft-fetch-event-source-a5c83ff95964) (not fetchable, 403)
- [Milan Jovanović: fetch-event-source: SSE With Bearer Tokens, Retries, and React](https://milanjovanovic.tech/blog/fetch-event-source)
- [nginx ngx_http_proxy_module docs](https://nginx.org/en/docs/http/ngx_http_proxy_module.html)
- [Cloudflare Community: SSE buffering issues](https://community.cloudflare.com/t/cloudflare-buffering-sse-streams/506921)
- [GitHub mastra-ai/mastra issue #13584: Cloudflare SSE buffering](https://github.com/mastra-ai/mastra/issues/13584)
- [GitHub cloudflare/cloudflared issue #1449: SSE over GET buffered on Quick Tunnel](https://github.com/cloudflare/cloudflared/issues/1449)
- [pkg.go.dev: http.Flusher](https://pkg.go.dev/net/http#Flusher)
- [pkg.go.dev: http.ResponseController](https://pkg.go.dev/net/http#ResponseController)
- [jackc/pgx GitHub](https://github.com/jackc/pgx)
- [brandur.org: The Notifier Pattern](https://brandur.org/notifier)
- [GitHub jackc/pgx issue #1121: pgxpool support for LISTEN](https://github.com/jackc/pgx/issues/1121)
- [Apple developer forums: EventSource error event not fired iOS 18](https://developer.apple.com/forums/thread/765183)
- [MagicBell: PWA iOS Limitations and Safari Support 2026](https://www.magicbell.com/blog/pwa-ios-limitations-safari-support-complete-guide) (no SSE content; not a source for §5)
- [server-sent-events.com: Mobile and Background-Tab Handling](https://www.server-sent-events.com/frontend-consumption-client-patterns/mobile-background-tab-handling/) (unsourced; claims struck)
- [OpenAI Responses API streaming docs](https://developers.openai.com/api/docs/guides/streaming-responses)
- [OpenAI: Background mode (stream resume with `starting_after`)](https://developers.openai.com/api/docs/guides/background)
- [OpenAI Community: Responses API streaming guide](https://community.openai.com/t/responses-api-streaming-the-simple-guide-to-events/1363122)
- [Claude Platform: Streaming messages](https://platform.claude.com/docs/en/build-with-claude/streaming)
- [DEV Community: Streaming Tool Calls parse Anthropic SSE](https://dev.to/gabrielanhaia/streaming-tool-calls-parse-anthropic-sse-without-loading-the-whole-message-2on)
- [Vercel AI SDK: Stream Protocols](https://ai-sdk.dev/docs/ai-sdk-ui/stream-protocol)
- [Vercel: Streaming Functions docs](https://vercel.com/docs/functions/streaming-functions)
- [AI SDK: Chatbot Resume Streams](https://ai-sdk.dev/docs/ai-sdk-ui/chatbot-resume-streams)
- [GitHub vercel/resumable-stream](https://github.com/vercel/resumable-stream)
- [LangSmith: Agent Server streaming (thread streams, `Last-Event-ID`)](https://docs.langchain.com/langsmith/streaming)
- [LangChain docs: Streaming in LangGraph](https://docs.langchain.com/oss/python/langgraph/streaming)
- [LangGraph reference: astream_events](https://reference.langchain.com/python/langgraph/stream)
- [Liveblocks blog: Introducing Liveblocks Sync (Feb 2026)](https://liveblocks.io/blog/introducing-liveblocks-sync-the-sync-engine-for-the-agentic-web)
- [Liveblocks docs: Storage & Sync Engine](https://liveblocks.io/docs/ready-made-features/multiplayer/sync-engine/liveblocks-storage)
- [WebSocket.org: SSE vs WebSocket comparison](https://websocket.org/comparisons/)
- [Ably blog: WebSockets vs SSE](https://ably.com/blog/websockets-vs-sse)
- [Security Boulevard: Bearer Tokens Explained 2026](https://securityboulevard.com/2026/01/bearer-tokens-explained-complete-guide-to-bearer-token-authentication-security/)
- [GitHub issue #522: Session bearer token SSE URL leakage](https://github.com/Traitome/oxo-flow/issues/522)
- [DEV Community: SSE Security and EventSource auth](https://dev.to/rxkov/server-sent-events-security-how-eventsource-breaks-your-api-authentication-model-3643)
- [OWASP CSRF docs](https://owasp.org/www-community/attacks/csrf)
- [Strix: CVE-2026-61593 djust CSRF SSE](https://www.strix.ai/cve/CVE-2026-61593)
- [Crosscheck: Cookies vs JWT Authentication 2026](https://crosscheck.cloud/blogs/cookies-vs-jwt-authentication-2026/)
- [Theodor Marcu (Substack): How ChatGPT streams responses back to the user](https://blog.theodormarcu.com/p/how-chatgpt-streams-responses-back) (author marks it outdated, Oct 2024)
- [DEV Community: Streaming LLM Responses SSE to Real-Time UI](https://dev.to/pockit_tools/the-complete-guide-to-streaming-llm-responses-in-web-applications-from-sse-to-real-time-ui-3534)
- [GitHub PR: Ava fix sharing SSE connections across browser windows](https://github.com/zhiyuan-zhang0206/Ava/pull/3312)
