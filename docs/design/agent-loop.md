# Agent Loop: Spec

Status: spec, 2026-09-26. Terms follow [`CONTEXT.md`](../../CONTEXT.md). The loop runs inside `exec(step)` of the Fold/Decide model; durable execution, Leases, waiting states, Steering, Interrupt and Budget checks are specified in [event-log.md](event-log.md) and are not redesigned here. Prompt projection and Compaction: [context.md](context.md). Approval Rules, Jev and the approval card: [approver.md](approver.md). Background, harness survey and sources: [../research/agent-loop.md](../research/agent-loop.md).

## 1. Summary

- The loop is `Load → Fold → Decide (pure, one Step) → exec(step)` (event-log.md §5.3–§5.4), repeated. This spec covers what `exec` does for each Step: build the prompt, call the Provider, run Tools, and what it appends.
- Only `ParallelSafe` tools (read-only built-ins, `memory view`, catalog Connector tools with `readOnlyHint`) run concurrently, capped at 4. Everything else runs one at a time, in the order the model asked.
- The Approver runs on every call in a turn's tool-use batch before any call starts. Calls it can't decide (rug-pull exception, or Jev unsure/unavailable/slow) go on one stepped approval card, one call at a time; approved calls from the card run in order once it's done.
- Hooks (`BeforeModel`/`AfterModel`/`BeforeTool`/`AfterTool`) are typed, pure with respect to storage: they return a verdict plus events; `exec` appends them. Decorators (retry, OTel, metering) wrap only `Provider`/`Tool`, never the step loop.
- LLM retries are ours (SDK retries = 0): a bounded in-process table, then a sleep for long waits, then a terminal error for key, billing, model, or size problems.

## 2. Scope

**In the base version**
- `exec(step)` for `StartTurn`, `RequestTools`, `StartTool` (batch or single), `MarkInterrupted`, `Park`, `Complete` (event-log.md §5.3).
- `Provider`, `Tool`/`ToolDef` (incl. `ParallelSafe`, `Timeout`), `Hook`, `Approver` contracts.
- Parallel-safe batching (WaitGroup + semaphore, cap 4); stepped per-call approval card; `approved_by` trace on `tool.call.started`.
- Tool-list freeze per assistant turn (hash on `turn.started`, full list as a blob).
- Thinking-block handling across Compaction and `/model` switches.
- LLM provider-error and retry policy; `max_tokens` cutoff handling; tool timeout and kill.
- Budget layering for LLM retries (quick retries → sleep/backoff → `failed`).
- MCP `input_required` multi round-trip; legacy `elicitation/create` in-process hold.
- Fake-Provider / fake-Tool test harness.

**Out (deferred)**
- Loop detection beyond the Budget turn limit (later: Gemini-CLI-style loop warning — verified against gemini-cli's `loopDetectionService.ts`: sha256 hash of each tool call's name+args, 5 repeats in a row triggers it).
- Hooks rewriting tool arguments (`updatedInput`).
- Automatic model fallback.
- Sandbox process-group kill mechanics in detail (future sandbox spec; referenced only).
- MCP client protocol details for `input_required` ([mcp-client.md](mcp-client.md); referenced only).

## 3. Data model

No new tables. `State`, `Fold`, `Decide` are defined in [event-log.md §5.4](event-log.md); this spec adds fields to existing payloads and to the `tool` package's types, used by `exec`.

**`ToolDef`** (new struct, `tool` package):

```go
type ToolDef struct {
    Name, Description string
    Schema             json.RawMessage
    ReadOnly           bool          // seeds ParallelSafe and Approver defaults from Connector hints
    Destructive        bool
    ParallelSafe       bool          // built-in read-only tools, memory view, catalog Connector readOnlyHint; cap 4 concurrent
    Untrusted          bool          // feeds memory taint (memory.md §5.2)
    Timeout            time.Duration // default 120s; shell defaults to the Sandbox wall-clock cap
    Source             string        // "builtin" | "connector:<id>"
}
```

**Extended event payloads** (both already exist in event-log.md's catalog; these are additive fields this spec relies on — needs a matching nod there):
- `turn.started` gains `tools_hash` (hash of the frozen `[]ToolDef` for this turn) and the full list stored as a blob, for replay (Decision 4).
- `tool.call.started` gains `approved_by` (`rule:<id>` | `jev` | `user` | `mode:<mode>`) and `trace` (e.g. Jev confidence) (Decision 3). No new event type.

**Derived, not stored**: `exec`'s parallel-safe batch grouping (§5.2) is computed each iteration from `State.PendingToolUse`/`State.Requested`; it is never persisted.

## 4. Contracts

### 4.1 Provider (streaming, callback style — Decision 14)

```go
type Provider interface {
    // Stream sends deltas to onDelta (UI only, via DeltaBus) and returns the final accumulated response.
    Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
    CountTokens(ctx context.Context, req Request) (int, error) // or estimate
    Models() []ModelInfo                                       // static catalog view: window, max output, pricing
    ListModels(ctx context.Context) ([]ModelInfo, error)        // live, per-key list for settings and the /model picker; runs in the Worker
}
type DeltaKind uint8 // 0 = text (zero value keeps today's callers correct); also thinking, tool_start, tool_args
type Delta struct {
    TurnID string
    Idx    int       // neutral part index within the assistant message
    Kind   DeltaKind
    Text   string    // text / thinking / partial tool-args JSON fragment
    CallID string    // Kind=tool_start|tool_args: our ToolUse.ID, so parallel calls stay separate
    Name   string    // Kind=tool_start: tool name, for the card title
}
type Response struct { Message msg.Message; Stop StopReason; Usage Usage; RequestID string }
type StopReason string // end_turn | tool_use | max_tokens | refusal | context_exceeded | malformed_call | other
```

No usage delta: usage is authoritative only on the final `Response` (event-log.md §5.11). `Request` gains `ResponseSchema json.RawMessage`: when set, the adapter requests a fixed JSON shape (Anthropic `output_config.format` / OpenAI `text.format` / Gemini `responseJsonSchema`) instead of forced tool use, for internal fixed-shape answers (Tidy) — forced tool use 400s on models like Opus 5.5/Fable 5.1 (§5.3). Adapter details: [provider-gateway.md](provider-gateway.md).

Callback over iterator: simpler to write and to fake in tests, and simpler to reason about under cancellation (research §7.2).

### 4.2 Tool / ToolDef

```go
type Tool interface {
    Def() ToolDef
    Call(ctx context.Context, in CallInput) (Result, error) // error = bug/infra; exec turns it into an is_error Result
}
type CallInput struct { CallID, IdempotencyKey string; Args json.RawMessage; Sandbox sandbox.Handle }
type Result struct { Content []msg.Part; IsError bool } // text + image etc.; not a single Part (provider-gateway.md §3.1)
type Registry interface {
    Defs(st SessionView) []ToolDef // deterministic order; frozen per assistant turn (Decision 4)
    Get(name string) (Tool, bool)
}
```

`ToolDef` fields are in §3. `ParallelSafe` and `Timeout` are read by `exec`'s `StartTool` handling (§5.2) and by the tool call's per-attempt context (§6).

### 4.3 Hooks

```go
type Hook interface {
    BeforeModel(ctx context.Context, st *State, req *llm.Request) (Verdict, error)
    AfterModel(ctx context.Context, st *State, resp *llm.Response) (Verdict, error)
    BeforeTool(ctx context.Context, st *State, call msg.ToolCall, def ToolDef) (ToolVerdict, error)
    AfterTool(ctx context.Context, st *State, call msg.ToolCall, res *tool.Result) (Verdict, error)
}
type Verdict struct {
    Events []eventlog.NewEvent // appended by exec in the same tx as the step's own events
    Park   *Park               // non-nil: stop and set status (e.g. awaiting_approval)
}
type ToolVerdict struct {
    Verdict
    Decision Decision // Allow | Ask | Deny
    By       string   // "rule:<id>" | "jev" | "user" | "mode:<mode>" -> stored as approved_by
    Trace    string   // e.g. Jev confidence -> stored as trace
    Reason   string   // shown to the model on Deny
}
type NopHook struct{} // embed to implement only the methods you need
```

A hook never writes to the DB and keeps no state (Decision 0). It returns a verdict; `exec` appends `Verdict.Events` in the same transaction as the step's own events, and the next `Decide` reads everything back from the log. There is no `UpdatedInput` field: hooks cannot rewrite tool arguments in the base version (Decision 13). Hooks run in registration order; for `BeforeTool` the first non-Allow wins.

### 4.4 Approver

```go
type Approver interface {
    Decide(ctx context.Context, st SessionView, call msg.ToolCall, def ToolDef) (ToolVerdict, error)
}
```

Implements `Hook.BeforeTool`. Runs in order: a rug-pull exception (a changed Connector tool, by `tool_hash`, always asks again, even in full-auto), then Approval Rules, then Jev (ADR 0004, amended 2026-09-27). Catalog Connector `readOnlyHint`/`destructiveHint` seed `ToolDef.ReadOnly`/`Destructive`, which seed the Approver's defaults. Hints from custom-URL Connectors are ignored: those tools default to ask and aren't parallel-safe ([mcp-client.md](mcp-client.md) Decision 18). Full spec, incl. the rule-suggestion threshold and the stepped card: [approver.md](approver.md).

If Jev is unsure, unavailable, or takes over 5s to answer, the verdict is `ask` with `reason: jev_unavailable`: the call waits for the user. Never allow on failure (Decision 18).

### 4.5 Decorators

C-style decorators (`func(Provider) Provider`, `func(Tool) Tool`) are allowed only below `Provider`/`Tool`: retry, OTel tracing, usage metering. They never wrap the step loop itself (Decision 0) — retries inside a decorator would hide attempts from the Event Log, which §5.3's retry table exists to prevent.

## 5. Algorithms and flows

### 5.1 Worked example: one turn, 2 parallel read-only tools + 1 shell tool

User asks: "Fetch these two docs and run the test suite." The model's `llm.response` requests, in order: `A=web_search` (`ParallelSafe`), `B=web_fetch` (`ParallelSafe`), `C=shell` (not parallel-safe, free under the Approver — it runs inside the Sandbox, ADR 0004). Event Log (session S1, continuing from seq 29):

| seq | type | actor | payload (key fields) |
|---|---|---|---|
| 30 | `user.message` | user:u1 | "Fetch these two docs and run the test suite" |
| 31 | `turn.started` | worker:w1 | turn_id=t7, model=fable-5.1, tools_hash=h1 |
| 32 | `llm.response` | worker:w1 | stop_reason=tool_use; tool_use=[A web_search, B web_fetch, C shell] |
| 33 | `tool.call.requested` | worker:w1 | A, web_search |
| 34 | `tool.call.requested` | worker:w1 | B, web_fetch |
| 35 | `tool.call.requested` | worker:w1 | C, shell |
| 36 | `tool.call.started` | worker:w1 | A, approved_by=jev:0.98 |
| 37 | `tool.call.started` | worker:w1 | B, approved_by=jev:0.95 |
| 38 | `tool.call.completed` | worker:w1 | B (finishes first; completion order = wall clock) |
| 39 | `tool.call.completed` | worker:w1 | A |
| 40 | `tool.call.started` | worker:w1 | C, approved_by=rule:sandbox-free |
| 41 | `tool.call.completed` | worker:w1 | C |
| 42 | `turn.started` | worker:w1 | turn_id=t8 |
| 43 | `llm.response` | worker:w1 | stop_reason=end_turn |
| 44 | `session.status_changed` | worker:w1 | running → awaiting_user |

### 5.2 Load → Fold → Decide → exec, iteration by iteration

1. **Load** events ≤29; **Fold**: `PendingUserSeqs=[30]`. **Decide**: `StartTurn`. **exec**: budget check, append 31 `turn.started{tools_hash}`, call `Provider.Stream`, append 32 `llm.response` + `usage.recorded` in one tx.
2. **Fold**: `PendingToolUse=[A,B,C]`. **Decide**: `RequestTools`. **exec** runs `BeforeTool` (the Approver) on **all three** pending calls before appending anything, per Decision 2 — none needs asking here, so it appends 33, 34, 35 (`tool.call.requested` × 3) in one tx. (If, say, `C` had needed asking — no rule matched, and Jev was unsure, unavailable, or slow — `exec` would still append all three `tool.call.requested`, plus `approval.requested{tool_call_id: C, reason: jev_unavailable}` for just that call, and park. The next `Decide` sees `OpenApproval != nil`, which parks ahead of `StartTool` in event-log.md §5.4's priority table, so `A` and `B` — already allowed — wait too. `C` shows on the session's one stepped approval card; the user resolves it (approve once / this project / everywhere, or deny with a reason). Had more than one call needed asking, each gets its own `approval.requested` in turn — the next claim re-requests the next unresolved ask call, advancing the card ("2 of N") — until every ask call is resolved (`approval.resolved`) or skipped (an earlier one was denied — no `approval.requested`/`.resolved` for a skipped call). Once that's done, the session is `runnable` again and `StartTool` runs `A`, `B`, `C` in the model's original order, same as if none had needed asking.)
3. **Fold**: `Requested={A,B,C: allowed, not started}`, `OpenApproval=nil`. **Decide**: `StartTool`. **exec** groups the maximal run of adjacent, allowed, not-started, `ParallelSafe` calls — here `{A,B}` — into one concurrent batch (WaitGroup + semaphore, cap 4; §6). `C` breaks the run (not `ParallelSafe`), so it is excluded from this batch. `exec` appends 36, 37 (`tool.call.started` × 2, each with `approved_by`) in one tx, then calls `A` and `B` concurrently; as each finishes it appends its `tool.call.completed` (38 then 39, completion order = wall clock, not request order).
4. **Fold**: `A`, `B` completed; `C` still allowed, not started. **Decide**: `StartTool` again. **exec**'s batch is `{C}` alone (size 1, since not `ParallelSafe`): appends 40 `tool.call.started{C}`, runs the shell command in the session's Sandbox, appends 41 `tool.call.completed{C}`.
5. **Fold**: all tool results in. **Decide**: `StartTurn`. **exec** appends 42 `turn.started`, calls the Provider, appends 43 `llm.response{stop_reason: end_turn}` + `usage.recorded`.
6. **Fold**: `end_turn`, no pending user message. **Decide**: `Complete`. **exec** appends 44 `session.status_changed{running→awaiting_user}` with `ExpectSeq`; lease cleared.

### 5.3 LLM retry policy (SDK retries = 0 — Decision 7)

Errors are classified into neutral classes (adapter's job, [provider-gateway.md](provider-gateway.md)), not raw status codes:

| Class | Examples | Action |
|---|---|---|
| `key_invalid` | 401/403 | stop → `session.error{retryable:false}` → `awaiting_user`; UI: "Update key" |
| `billing` | 402, org/workspace spend limit (incl. Anthropic 400 spend-limit), spend-cap 429 without `retry-after` | stop → `session.error{retryable:false}` → `awaiting_user`; UI: "Open provider billing" / "Switch model" |
| `model_unavailable` | 404, model not allowed for this key | stop → `session.error{retryable:false}` → `awaiting_user`; UI: "Switch model" |
| `rate_limited` | 429 with `retry-after` ≤ 60s | up to 4 in-process attempts, honoring `retry-after`, respecting ctx |
| `long_wait` | `retry-after` > 60s | `timer.set{wake_at}` → `sleeping` (frees the Worker) |
| `provider_down` | 500/529/504, stream drop, 300s stream idle timeout | quick retries (as `rate_limited`), then the existing 1/5/15 min Layering |
| `too_large` | 413 | stop → `session.error{retryable:false}` → `awaiting_user`; UI: "File too large" → "Remove file" |
| `bug` | our malformed request (e.g. Anthropic 400 with `block_binding`/`thinking`/`tool_choice` markers) | stop → `session.error{retryable:false}` → `awaiting_user`; UI: "Internal error (ID …)"; log the request ID |

Unknown codes: 5xx → `provider_down`; anything else → `bug`. Every "stop" action maps to `session.error{retryable:false}` → `awaiting_user`, as today.

While sleeping, the user can "Stop retrying" (an Interrupt; event-log.md §10) → `awaiting_user`, or "Retry now" → `runnable`. A new user message also wakes it. The backoff step comes from counting `session.error{retryable:true}` since the last `user.message`, so it survives a Worker crash (amended 2026-10-02).

Anthropic's `model_context_window_exceeded` is a `StopReason` (`context_exceeded`) on a successful response, not an error — it never appears in this table. OpenAI's `context_length_exceeded` 400 still routes to Compaction Tier 2 ([context.md](context.md) §5.3), then retry once.

**Layering** (Decision 7, today): if all 4 quick attempts fail, append `session.error{retryable:true}`; the session sleeps and retries at 1 min, 5 min, 15 min; if that also fails, `session.error{retryable:false}` → `failed`, and notify the user on their Channels. Each failed attempt that reported usage still appends `usage.recorded`.

**OpenAI:** always send `truncation: "disabled"`. With `auto` OpenAI silently drops the oldest items and bypasses Compaction. The overflow check runs before generation, so it is always an upfront 400, never mid-stream and never in `incomplete_details` (Decision 20).

Unknown finish/stop-reason enum values (any provider) map to `StopReason: other`, logged. Gemini `MISSING_THOUGHT_SIGNATURE`, cited in research §3.1's stop-reason table, is not in go-genai's `FinishReason` enum today — treat it as unverified until confirmed on the wire, and fall back to `other` + log if seen.

### 5.4 Interrupt during a parallel batch

Mid-batch (state after seq 36/37, before 38/39 in §5.1): `user.interrupt` cancels the shared `ctx`. Per call:
- Started, not yet completed (both `A` and `B` here): `tool.call.interrupted{user_interrupt}` for each, rendered as `is_error "interrupted by user after Ns; it may have partially run"`.
- Requested, not started (`C`, if the batch hadn't reached it yet): `tool.call.completed{is_error, "not run: interrupted by user"}`.

**MCP calls** (Interrupt or timeout): cancel the call's `ctx`. The official Go SDK sends `notifications/cancelled` on stdio; on Streamable HTTP closing the stream is the cancel. Servers may ignore it, so the side effect may still happen; ignore any late response. Recorded as above with "it may have partially run" (Decision 21).

Then `Park(awaiting_user)`. Queued Steering is not auto-sent (Decision 10).

### 5.5 Tool-list freeze across a turn

`turn.started.tools_hash` is computed from `Registry.Defs` once, at the start of the turn, and reused for every step inside that turn (including a re-run after a crash). A Connector added or an MCP `list_changed` mid-turn does not change `Defs` until the next user message. Replay reads the full list from the blob referenced by that turn, never recomputes it (Decision 4).

### 5.6 MCP `input_required` mid tool call

A connector's tool call returns `input_required`: `exec` appends `elicitation.requested{connector, schema, request_state}` (reusing the existing event type, event-log.md §4), parks `awaiting_user`, and releases the Worker. `elicitation.resolved{action, content?}` resumes the same tool call: the resume re-sends the original args plus the answer and the echoed `request_state`, under the same `tool_call_id`/`idempotency_key`. Legacy `elicitation/create` does not park: it holds the Worker in-process for up to 5 minutes, then the call ends `tool.call.completed{is_error, "needs user input; timed out"}`.

Inside a parallel batch: sibling calls finish and their `tool.call.completed` events are appended first; then the session parks. After the answer only the asking call resumes; finished siblings never re-run (Decision 19).

## 6. Rules and invariants

- A batch is the maximal run of adjacent, allowed, not-started, `ParallelSafe` calls (cap 4); everything else runs alone, in the order the model asked (Decision 1).
- Use `sync.WaitGroup` + a semaphore for a batch, never `errgroup.WithContext` — its ctx cancels on the first non-nil error, which would kill healthy siblings (research §3.2).
- The Approver runs on every pending tool_use of a turn before any call starts; any call it can't decide parks the whole batch behind one stepped approval card — approve once / this project / everywhere or deny (with a reason) per call, "approve all" available — before any `StartTool` (Decision 2, via event-log.md's `OpenApproval` priority — §5.2).
- `approved_by` + `trace` live on `tool.call.started`, never a new event type (Decision 3).
- Log order is completion order; the prompt projection re-sorts tool results into tool_use order before sending them to the Provider (context.md handles the projection; not restated here).
- Execute a tool only from its final accumulated args, never a partial streamed delta; unparseable or invalid args → `is_error "invalid arguments: …"`.
- A started tool call is never retried automatically; every failure (exception, timeout, denial, bad args, unknown tool) becomes `tool.call.completed{is_error:true}`.
- Tool timeout: 120s default; shell defaults to the Sandbox wall-clock cap; `ToolDef.Timeout` overrides. On timeout the Sandbox kills the process group (TERM, grace, KILL); the model gets a readable error; never auto-retried (Decision 11; kill mechanics: future sandbox spec).
- After a Compaction clear or summary, drop every thinking block after the first edited spot; do not send the `drop_block` beta (Decision 5).
- After `/model` to a different provider, drop foreign thinking, keep only text and tool calls; on Gemini, add the dummy signature to old function calls (Decision 6).
- SDK-level retries are disabled; the loop owns retries per §5.3 (Decision 7). No automatic model fallback (Decision 8).
- `max_tokens` cutoff: drop any incomplete `tool_use`, keep the text, `Park(awaiting_user)`; no retry, since every request already asks for the model's max output (Decision 9, amended 2026-10-03).
- Hooks never write to storage and keep no state between calls; only `exec` appends (Decision 0).
- Decorators wrap only `Provider`/`Tool`, never the step loop (Decision 0, §4.5).
- Hooks cannot rewrite tool arguments in the base version (Decision 13).
- Budget: turn limit resets with each `user.message`; tokens and dollars accumulate over the whole session; the time limit applies only to Background Sessions, Child Sessions and System Sessions, counting only `running` time. Budget Allow adds the same amount again; Deny → `awaiting_user`.

## 7. Events

`exec` appends events per event-log.md's catalog (§4) and Decide table (§5.4); this table is the Step-to-event mapping this spec relies on, not a new catalog.

| Step | Events appended | Notes |
|---|---|---|
| `StartTurn` | `turn.started`, `llm.response`, `usage.recorded` | `turn.started` carries `tools_hash` (§5.5) |
| `RequestTools` | `tool.call.requested` × N (whole pending batch) | Approver runs per call first; no event for "allow" |
| `StartTool` (batch or single) | `tool.call.started` × batch size, `tool.call.completed` × (as each finishes) | `tool.call.started` carries `approved_by` + `trace` |
| Ask gate | `approval.requested{kind:tool}` + `approval.resolved`, one call at a time (stepped card, [approver.md](approver.md) §5.8) | parks `awaiting_approval` before any `StartTool` in the batch; resumes once every ask call in the batch is resolved or skipped |
| Deny | `tool.call.completed{is_error:true, denied:true}` | no `approval.requested` |
| `MarkInterrupted` | `turn.interrupted` or `tool.call.interrupted` | one per open turn/tool (event-log.md §5.5) |
| Budget check | `budget.exceeded`, `approval.requested{kind:budget}` | before `StartTurn`/`StartTool`; tokens/dollars re-checked after `llm.response` |
| Provider retry exhausted | `session.error{retryable}`, `timer.set` | §5.3 |
| Compaction trigger | `context.cleared`, `compaction` | [context.md](context.md) §7; not restated |
| MCP `input_required` | `elicitation.requested`, `elicitation.resolved` | reuses the existing catalog rows |
| `Complete` | `session.completed`, `session.status_changed` | event-log.md §4 |

## 8. UI

- A tool-call batch pending approval is shown as one stepped card per session: the calls that need a decision, one at a time in the model's order — Approve once / this project / everywhere / Deny (optional reason) per call, plus "Approve all". A deny closes the card; later calls on it are skipped. Child Sessions get their own card, labelled with the Agent name (§5.2).
- Each tool call chip shows its `approved_by` trace (rule id / Jev confidence / user / mode), in the same spirit as memory write chips ([memory.md](memory.md) §8).
- Timeout and interrupt errors render the exact model-facing text from §5.4/§6 in the transcript, not a generic failure.
- Terminal provider errors (`key_invalid`, `billing`, `model_unavailable`, `too_large`, `bug`) show the class-specific action text (§5.3) and, where applicable, a `/model` suggestion; there is no fallback-model picker.
- Compaction divider, memory chips, and budget/approval UI are defined in [context.md](context.md) §8, [memory.md](memory.md) §8, and event-log.md; not restated here.

## 9. Decisions

All accepted 2026-09-26.

0. **Loop shape: a step machine.** `Load → Fold → Decide (pure, one Step) → exec (appends events)`, repeated. Hooks are typed (`BeforeModel`/`AfterModel`/`BeforeTool`/`AfterTool`), return a verdict plus events, never write to the DB, never keep state. C-style decorators are allowed only below `Provider`/`Tool` (retry, OTel, metering).
1. **Parallel tools.** Only `ParallelSafe` tools (read-only built-ins such as web search/fetch and `memory view`, plus catalog Connector tools with `readOnlyHint`) run concurrently, max 4 at once. Everything else (shell, file edits) runs one at a time, in the order the model asked. WaitGroup + semaphore, not `errgroup.WithContext`. Hints from custom-URL Connectors are ignored: those tools default to ask and aren't parallel-safe ([mcp-client.md](mcp-client.md) Decision 18).
2. **Batch with an "ask".** Run the Approver on every call in the turn's batch first. Calls it can't decide (rug-pull exception, or Jev unsure/unavailable/slow) go on one stepped approval card — one call at a time, in the model's order, with approve once / this project / everywhere / deny (+ reason), and an "approve all" shortcut; a deny closes the card and skips the rest. Pause before starting any call in the batch until the whole card is resolved, then run the batch in the model's order.
3. **Approved-by trace.** Allowed decisions are recorded in the `tool.call.started` payload as `approved_by` (rule id / jev / user / mode) plus a trace (e.g. Jev confidence). No new event type.
4. **Tool-list freeze.** Tool list changes (Connector added, MCP `list_changed`) are frozen for the current assistant turn and applied at the next user message. `turn.started` records a hash of the tool list; the full list is stored as a blob for replay. One cache miss per change is accepted.
5. **Thinking vs Compaction.** After any clear or summary, the projection drops every thinking block after the first edited spot. Don't send the `drop_block` beta.
6. **Foreign-provider thinking after `/model`.** Drop it, keep only text and tool calls. On Gemini, add the dummy signature to old function calls. Never paste another model's reasoning in as text.
7. **LLM retries are ours.** SDK retries = 0. Neutral error classes (§5.3, research §3.6, [provider-gateway.md](provider-gateway.md) §8.2): `rate_limited` → up to 4 in-process attempts honoring `retry-after` (≤60s). `long_wait` → `timer.set` → `sleeping`. `provider_down` → quick retries, then the Layering below. `key_invalid`/`billing`/`model_unavailable`/`too_large`/`bug` → stop → `session.error{retryable:false}` → `awaiting_user`, each with its own UI action (§5.3). Context window full → Compaction Tier 2, then retry once: Anthropic reports it as `StopReason` `context_exceeded` on a successful response; OpenAI as a `context_length_exceeded` 400, treated the same. **Layering**: if the 4 quick attempts all fail → `session.error{retryable:true}`, the session sleeps and retries after 1 min, 5 min, 15 min; then `failed` and notify the user on their Channels.
8. **No automatic model fallback** (BYOK). Show the error, suggest `/model`.
9. **`max_tokens` cut-off.** Drop any incomplete `tool_use`, keep the text, `awaiting_user`. No retry: every request already asks for the model's max output, so a retry would hit the same cap (amended 2026-10-03, S6 grill; was: retry once at max output).
10. **After an Interrupt**, don't auto-send queued Steering. Go to `awaiting_user`; queued text becomes the next normal message. Mid-stream: `turn.interrupted` (partial text omitted from the prompt; marker `[Previous turn interrupted by user]` merged into the next user message). Started tools: `tool.call.interrupted`. Requested but not started: `tool.call.completed{is_error, "not run: interrupted by user"}`. Also decided: Interrupt when the session is waiting does nothing, EXCEPT `awaiting_children`, which stops all children and moves the parent to `awaiting_user`. Interrupting a parent always propagates to its Child Sessions.
11. **Tool timeout**: 120s default. Shell up to the Sandbox wall-clock cap; `ToolDef.Timeout` overrides. On timeout the Sandbox kills the process group itself (Docker can't kill an exec, moby#35703): TERM, grace, KILL. The model gets a readable error ("timed out after Ns; the command may have partially run"). Never auto-retry. (Kill mechanics belong to the future sandbox spec; referenced only here.)
12. **Loop detection: later.** The Budget turn limit covers it now. Later idea (deferred): Gemini-CLI style — sha256 hash of each tool call's name+args, 5 repeats in a row → warn, then pause (`loopDetectionService.ts`).
13. **Hooks can't rewrite tool args** (no `updatedInput`) in the base version.
14. **Streaming API**: callback style, `Stream(ctx, req, onDelta func(Delta)) (*Response, error)`.
15. **Budget** (relevant to this loop's checks). Turn limit resets with each user message. Tokens and dollars accumulate over the whole session. Time limit applies only to Background Sessions (entered only via `/background`), Child Sessions and System Sessions, never foreground chat; the clock starts at 0 on `/background`, counts only `running` time, and ends when the agent finishes (clears `background`, notifies the user on their Channels) or the user's next message brings the session back to foreground. Budget Allow adds the same amount again; Deny → `awaiting_user`.
16. **MCP input during a tool call.** Support MCP `input_required` (multi round-trip): save the question (`connector, schema, request_state`), release the Worker (`awaiting_user`), resume the tool call on `elicitation.resolved{action, content?}` by re-sending the original args plus the answer and the echoed `request_state`, same `tool_call_id`/`idempotency_key`. Legacy `elicitation/create` holds the Worker in-process up to 5 min, then the call ends `is_error "needs user input; timed out"`. (Full MCP client behavior: [mcp-client.md](mcp-client.md), referenced only.)
17. **Adopted from research** (not separately grilled; listed here per item, unsure parts moved to §12 Open gaps):
    - Stop-reason table (research §3.1): refusal → record, `awaiting_user`, no auto-retry; malformed call → retry once then `session.error`; empty `end_turn` after tool results → continuation nudge.
    - Log order = completion order; the projection re-sorts tool results into tool_use order (research §3.2).
    - Execute only from the final accumulated args; invalid → `is_error "invalid arguments: …"` (research §3.4).
    - A 3rd, moving cache breakpoint on the last block of each request; `prompt_cache_key = session_id` for OpenAI (research §3.7).
    - Steering placement: tool results first, then steering text, in one user message. Steering arriving during the final LLM call → `StartTurn` instead of parking (research §4).
    - Prompt block order per research §6 (extends [context.md](context.md) §5.2).
    - Package layout, interfaces, cancellation with `WithCancelCause` + `WithoutCancel` for the interrupted append, goroutine rules (research §7).
    - Fake-Provider testing style (research §7.4, §11 below).
    - A started tool call is never auto-retried; all tool failures → `tool.call.completed{is_error}`.
18. **Approver check failure** (Jev unsure, unavailable, or over 5s) → `ask`, `reason: jev_unavailable`. Never allow on failure.
19. **`input_required` in a parallel batch:** siblings finish and are recorded, then park; after the answer only the asking call resumes.
20. **OpenAI context overflow:** always `truncation: "disabled"`; a 400 with code `context_length_exceeded` or a message mentioning context length/window → treated as `context_exceeded` (Compaction Tier 2, retry once), same as Anthropic's stop-reason handling (§5.3).
21. **MCP cancel:** cancel `ctx` (Go SDK handles stdio notify / HTTP stream close); ignore late responses; never assume the side effect stopped.
22. **`/model` is logged** as `session.config_changed{model}` (API, unfenced); the next turn uses it and replay reproduces it (2026-09-27, [agents-skills.md](agents-skills.md) D18).

## 10. Edge cases

- **Refusal stop reason**: record it, `Park(awaiting_user)`, no auto-retry.
- **Malformed tool call** (e.g. Gemini `MALFORMED_FUNCTION_CALL`): retry once, then `session.error`.
- **Empty `end_turn` right after tool results**: append a continuation nudge as the next user-role message rather than retrying the identical request.
- **`max_tokens` cut off**: keep the truncated text, drop the cut-off `tool_use`, `Park(awaiting_user)`; no retry.
- **Context window full mid-batch**: force Compaction Tier 2 before the retried `StartTurn`; in-flight tool results are unaffected (they're already appended).
- **Interrupt lands mid-parallel-batch**: started calls → `tool.call.interrupted`; not-yet-started calls in the same batch → `tool.call.completed{is_error, "not run"}` (§5.4).
- **Tool-list change mid-turn**: ignored until the next user message; `turn.started.tools_hash` and the blob make replay consistent even if the live registry has since changed.
- **Thinking block after a Tier 1 clear**: every thinking block after the cleared spot is dropped from the projection, even if the underlying model would accept it.
- **Model switched mid-session (`/model`)**: prior-provider thinking is dropped from the next request; Gemini receives the dummy signature on old function calls.
- **MCP legacy `elicitation/create` times out**: the tool call ends `is_error "needs user input; timed out"`; the Worker was held the whole time, so no `awaiting_user` park occurred.
- **Background Session backgrounded then brought to foreground mid-turn**: the wall-clock Budget clock stops counting from that point (only `running` time in the background state counts).
- **Budget denied mid-batch**: the check happens before `StartTool`, not mid-flight; a batch already started is allowed to finish before the next check applies the deny.
- **Terminal provider error (`key_invalid`/`billing`/`model_unavailable`/`too_large`/`bug`) on a retry attempt that already reported partial usage**: `usage.recorded` is still appended for that attempt before `session.error{retryable:false}`.

## 11. Acceptance criteria

- Fake-Provider fixture with one `llm.response` containing tool_use `[A(ParallelSafe), B(ParallelSafe), C(not-parallel)]`, all Approver-allowed: golden event sequence is `requested×3, started{A,B}, completed×2 (either order), started{C}, completed{C}`, matching §5.1's table shape. `p.Requests()` shows exactly 2 `StartTurn` calls (before and after the tool batch).
- Same fixture with `B`'s Approver verdict = Ask: no `tool.call.started` appears anywhere in the log until `B`'s card entry is resolved (`approval.resolved`); then the full three-call batch starts, in the model's order.
- Concurrency test: a fake Tool for `A`/`B` blocks until both have started, proving they ran inside one semaphore-bounded goroutine group; a fake Tool for `C` asserts it started strictly after both `A` and `B` completed.
- `go test -race` plus goleak on the batch runner: no goroutine outlives `exec`'s return for that step.
- A fake Provider returning `Overloaded` 4 times then success: exactly 4 retry attempts recorded, honoring a scripted `retry-after`, before the successful `llm.response`.
- A fake Provider returning `Overloaded` on all 4 attempts: `session.error{retryable:true}` is appended, and a scripted clock shows the next claim eligible at `wake_at` = now+1min (then +5min, +15min on repeat failure), ending `failed` with a Channels notification.
- A fake Provider returning a 401: `session.error{retryable:false}` appended immediately (no retries), status `awaiting_user`.
- A fake Provider returning a truncated tool_use (`max_tokens`): the tool_use is dropped, the text kept, status `awaiting_user`, and no second Provider call.
- A fake Tool that sleeps past its `ToolDef.Timeout`: `tool.call.completed{is_error, "timed out after Ns…"}`, and the call is never retried automatically.
- Interrupt fired mid-batch (fake Tool `BlockUntilCancel` for `A`, `B` already completed, `C` not yet started): log ends with `tool.call.interrupted{A}`, `tool.call.completed{C, is_error, "not run: interrupted by user"}`, `turn.interrupted` or `Park(awaiting_user)` per §5.4.
- Tool-list change fixture: a Connector's `tools_hash` differs mid-turn; the turn in progress uses the frozen blob; a new turn (after the next `user.message`) uses the updated `Defs()`.
- Thinking-drop fixture: a `compaction` event at seq K; projecting a prompt for seq > K contains no thinking block dated before the first edited spot.
- `/model` switch fixture: projecting to a different provider drops all prior-provider `ThinkingPart`s; a Gemini destination adds the dummy signature to old function-call parts.
- `RequestTools` golden test: given 3 pending tool_use blocks, `exec` appends all 3 `tool.call.requested` in one transaction (one `seq` range), never interleaved with a `StartTool` for an earlier one.
- MCP `input_required` fixture: `elicitation.requested{connector, schema, request_state}` parks `awaiting_user`; `elicitation.resolved{action, content?}` resumes the same tool call, re-sending the original args plus the answer and the echoed `request_state` (`tool_call_id`/`idempotency_key` unchanged) rather than starting a new one.
- Legacy `elicitation/create` fixture: after a scripted 5-minute clock, the call ends `tool.call.completed{is_error, "needs user input; timed out"}` with no status-parking event.
- Budget fixture: a Background Session's wall-clock counter does not advance while `awaiting_user`/`sleeping`; it resumes counting only after the next claim sets it `running`.

## 12. Open gaps

None.

**Deferred to other specs** (tracked in GitHub issues; Sandbox: [#23](https://github.com/bhanuprakaash/jelly-fish/issues/23)):
- Full MCP client behavior (transports, OAuth, Connector setup) → [mcp-client.md](mcp-client.md).
- Process-group kill timings (TERM → grace → KILL) → sandbox spec.

## 13. Research

Harness survey (Claude Agent SDK, OpenAI Agents SDK, Codex CLI, Gemini CLI, LangGraph, Letta, Pydantic AI, Vercel AI SDK), loop mechanics (§3), Steering/Interrupt precedent (§4), hooks (§5), prompt assembly (§6), Go design (§7), options considered (§8), and all sources: [../research/agent-loop.md](../research/agent-loop.md).
