# Context Management and Compaction: Spec

Status: spec, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Background, vendor survey and rejected options: [../research/context.md](../research/context.md). Cross-session memory (Project Memory, User Memory, the `memory` tool): [memory.md](memory.md).

## 1. Summary

- In-session context management: two-tier **Compaction**. Tier 1 clears old tool results; Tier 2 replaces old turns with an LLM summary.
- Both tiers are recorded as Event Log events. The Event Log is never modified; the prompt sent to the model is a pure projection of it.
- Summaries are plain text in our neutral format, written by the session's current model on the user's Provider Key.
- Before Tier 2, the agent gets one memory flush turn to save durable facts.
- Child Sessions compact independently on their own log.

## 2. Scope

**In the base version**
- `context.cleared` and `context.compacted` events.
- The prompt projection function and cache-stable prompt layout.
- Tier 1 (tool-result clearing), Tier 2 (summary), hard ceiling truncation.
- `/compact [instructions]` Slash Command.
- Memory flush turn.
- Child Session result cap.
- Thinking-block clearing after any clear or summary, and on a foreign-provider `/model` switch.

**Out**
- Vendor server-side compaction or context editing (Anthropic, OpenAI). Not used.

## 3. Data model

No new tables. State lives in the Event Log as two new event types (neutral format):

```
context.cleared   {through_seq, cleared_tool_call_ids[]}              -- Tier 1
context.compacted {covers_through_seq, summary_text, tokens_before,
                   tokens_after, model, trigger: auto|manual,
                   flush_skipped: bool}                                -- Tier 2
```

- `trigger=manual` comes from `/compact`; `trigger=auto` from the threshold.
- `flush_skipped=true` when the memory flush turn was skipped because it would overflow the window (§5.4).
- The prompt is derived from these plus the rest of the log. Nothing else stores compacted state.

## 4. API contracts

**Slash Command `/compact [instructions]`**
- Runs Tier 2 now, regardless of thresholds, with optional user instructions passed to the summarizer.
- Emits `context.compacted {trigger: manual}`.

**Projection function** (pure, deterministic):

```
(agent config, project instructions, memory index, events, latest compaction, clears) → neutral messages
```

Replay after a crash calls the same function, so a new Worker rebuilds the same prompt.

**Tier 1 placeholder text** for a cleared result:

```
[result cleared — call id X; ask to re-run]
```

**Memory flush turn text**:

```
Context will be compacted. Save any durable facts with the memory tool now.
```

**Tier 2 summary template sections** (fixed): Goal · Decisions · Current state · Open tasks / next steps · Pending approvals · Child sessions (id, status) · Files / Artifacts touched · User preferences stated.

## 5. Algorithms and flows

### 5.1 Projection
Build the prompt from the log each turn via the projection function (§4). Apply the latest `context.compacted` (projection starts at its summary) and all `context.cleared` events (swap cleared results for the placeholder). Never recompute clears or summaries on replay; read them from events.

**Upload projection**: an Upload stays inline (base64 from our blob) for the next 3 user messages after it was attached. At the first user-message boundary after that (or at Compaction, if earlier), the projection swaps it for a stub note (e.g. `[report.pdf, 42 pages, 3 MB. Removed from view; call read_upload("up_7") to read it]`) — never mid tool loop. The Event Log is unchanged; the swap is deterministic and costs one accepted cache break. The model re-reads the file via the built-in tool `read_upload(id, pages?)`. Adapter-side transport of the inline bytes is [provider-gateway.md](provider-gateway.md)'s concern, not this projection's. A 24 MB inline budget (total base64 size of inline Uploads) can stub the oldest inline Upload early, ahead of its N=3 turns, and `read_upload` results are stubbed the same way after 3 user messages, oldest first ([uploads-artifacts.md](uploads-artifacts.md)).

### 5.2 Prompt layout
Order, for cache stability:

1. tools
2. system (platform + Agent instructions + Project instructions)
3. `<user_memory>` + `<project_memory>` index ([memory.md](memory.md) §5.1) + project files index (name, id, size; read via `read_upload`, [provider-gateway.md](provider-gateway.md) Decision 23) + Skill index (name + description of each Skill in the Agent snapshot; bodies load via `load_skill`, [agents-skills.md](agents-skills.md) §4.2)
4. **[cache breakpoint]**
5. latest compaction summary
6. **[cache breakpoint]**
7. live turns
8. **[cache breakpoint]** — moving: placed on the last block of each request, so the growing tail is cached too.

- Keep tools and system byte-stable. No timestamps or other volatile data before a breakpoint.
- The memory index refreshes only at session start and after a Compaction, never mid-session.
- The project files index updates when project files change; the one cache break is accepted (rare). Replay rebuilds it from the Project Files that existed at the turn's `turn.started` time ([uploads-artifacts.md](uploads-artifacts.md) Decision 24).
- OpenAI: also set `prompt_cache_key = session_id` on every request.
- A Child Session uses the same layout with its own Agent's instructions and Skill index; its first user message is the `delegate` task. It never contains the parent's conversation ([agents-skills.md](agents-skills.md) §5.3).

### 5.3 Compaction tiers
Let W = the context window of the *current* model and T = the projected prompt tokens (from the adapter's token counter, or an estimate).

1. **Tier 1: tool-result clearing**, when T > 0.6·W.
   - Replace the results of all but the last 5 tool calls with the placeholder. Keep the call inputs.
   - Skip excluded tools (`memory`, `delegate` results).
   - Only if this frees ≥ 15% of W. Otherwise skip Tier 1 (no event); Tier 2 still triggers only at 0.75·W. There is no early Tier 2.
   - Append a `context.cleared` event.
2. **Tier 2: summary**, when still T > 0.75·W, or on `/compact [instructions]`.
   1. Memory flush turn (§5.4), unless it would overflow the window.
   2. Pick a split point that keeps the last K=4 turns word for word. Never split a tool call from its result, or an open Approval.
   3. Call the session's current model (user's key, recorded as Usage) with the fixed template (§4). Include the previous compaction summary as input, so summaries chain.
   4. Append a `context.compacted` event. The projection now starts at that summary.
   5. Re-inject: invoked Skill bodies (cap 5k tokens each) and the memory index (refreshed).
3. **Hard ceiling**: if a single turn still doesn't fit (a huge result), truncate that result in the projection with a pointer to the full event.
4. **Child Sessions** (started by Delegation via the `delegate` tool) run the same algorithm on their own log. The child's final result to the parent is capped at 2,000 tokens by default, configurable per Agent, and instructed to be a distilled summary. Longer output is saved as an Artifact and linked in the result.

Thresholds (0.6 / 0.75 / K=4 / 5 results / 15%) and the 2,000-token child result default are starting values to tune with the eval suite.

### 5.4 Memory flush turn
Before Tier 2, inject one turn with the flush text (§4).
- At most one turn; it counts against the Budget.
- Writes go through the normal `memory` tool and rules, including taint review ([memory.md](memory.md) §5.2, §5.3).
- Skipped in incognito sessions and in Child Sessions (they can't write memory; their result to the parent carries what matters).
- Skipped if the flush turn itself would overflow the window: summarize immediately and record `flush_skipped: true` on the `context.compacted` event.

### 5.5 Thinking-block clearing

After any clear or summary — Tier 1 tool-result clearing, Tier 2's keep-last-K turns, or `/clear` — the projection drops every thinking block after the first edited spot in the transcript, because Fable 5.1/Opus 5.5 prefix binding invalidates a thinking block once anything earlier in the sequence changes.

- Never send the `drop_block` beta; dropping happens in our own projection, not via a vendor flag.
- On `/model` to a different provider, the new model never sees the old model's thinking blocks: they are dropped, keeping the text and tool_use/tool_result blocks. Gemini additionally needs a dummy signature attached to old function calls to replay them.
- Never paste a dropped model's reasoning back as plain text; it is discarded, not repurposed.

## 6. Rules and invariants

- Never delete or rewrite Event Log events for Compaction.
- The prompt is always a projection of the log; replay must produce the same prompt.
- Compaction and clearing are always recorded as events, never recomputed differently on replay.
- Never use vendor server-side compaction or opaque vendor artifacts; summaries are plain text in our format (ADR 0002).
- Thresholds are fractions of the current model's window, not fixed token counts (the user can switch model mid-session).
- Compaction LLM calls use the user's Provider Key and are recorded as Usage, counting against the Budget.
- Never clear `memory` or `delegate` tool results in Tier 1.
- Never split a tool call from its result, or an open Approval, when picking the Tier 2 split point.
- Compact rarely and in big chunks (Tier 1 only when it frees ≥ 15% of W).
- Never put volatile data (timestamps etc.) before a cache breakpoint.
- The memory index is never refreshed mid-session.
- At most one memory flush turn per Tier 2; none in incognito or Child Sessions, and none when it would overflow the window (`flush_skipped: true`).
- Tier 2 never triggers below 0.75·W except via `/compact`, even when Tier 1 was skipped.
- A Child Session's result to the parent never exceeds its Agent's cap (default 2,000 tokens); overflow goes to an Artifact linked in the result.
- Each Child Session has its own log, context window and Compaction; the parent sees only the child's capped result.
- After any clear or summary, thinking blocks after the first edited spot are dropped from the projection; foreign-provider thinking is always dropped on `/model`, never pasted back as text. Never send the `drop_block` beta.
- A third, moving cache breakpoint sits on the last block of each request, in addition to the two static ones. OpenAI requests also set `prompt_cache_key = session_id`.

## 7. Event types emitted

- `context.cleared {through_seq, cleared_tool_call_ids[]}`: Tier 1.
- `context.compacted {covers_through_seq, summary_text, tokens_before, tokens_after, model, trigger: auto|manual, flush_skipped}`: Tier 2.
- Memory flush writes emit `memory.written` as defined in [memory.md](memory.md) §7.

## 8. UI touchpoints

- `/compact [instructions]` Slash Command in the chat input.
- Memory flush writes show as normal memory chips ([memory.md](memory.md) §8).
- **Compaction divider**: after a `context.compacted`, the chat shows a divider "Earlier messages summarized · view summary" (opens `summary_text`). The full history stays scrollable above it, because the Event Log is never mutated.

## 9. Decisions

All decided 2026-09-24.

1. **Our own Compaction, stored as events.** The Event Log is never modified; the prompt is a projection. No vendor server-side compaction. Why: breaks model switching; OpenAI's is opaque (research §3).
2. **Tier 1 clears tool results at 0.6·W. Tier 2 summarizes at 0.75·W. Keep the last K=4 turns word for word.** Starting values, tuned with evals.
3. **One memory flush turn before Tier 2.**
4. **The summarizer is the session's current model** (user's key, recorded as Usage).
5. **Child Sessions run the same algorithm on their own log.**
6. **Child Session result cap**: 2,000 tokens by default, configurable per Agent. Longer output is saved as an Artifact and linked in the result.
7. **No early Tier 2.** If Tier 1 is skipped because it would free < 15% of W, Tier 2 still triggers only at 0.75·W.
8. **Flush overflow.** If the memory flush turn itself would overflow the window, skip the flush, summarize immediately, and record `flush_skipped: true` on the `context.compacted` event.
9. **Compaction UI.** The chat shows a divider "Earlier messages summarized · view summary". The full history stays scrollable, because the Event Log is never mutated.
10. **Delegation terminology.** Child Sessions are started by Delegation through the `delegate` tool, as defined in CONTEXT.md.

Accepted 2026-09-26:

11. **Rename event `compaction` → `context.compacted`**, in the catalog and everywhere it is referenced.
12. **Thinking-block clearing.** After any clear or summary (Tier 1, Tier 2's keep-last-K, `/clear`), drop every thinking block after the first edited spot (Fable 5.1/Opus 5.5 prefix binding invalidates them). Never send the `drop_block` beta. On `/model` to a different provider, drop the old model's thinking blocks (keep text and tool calls); Gemini gets a dummy signature on old function calls; never paste another model's reasoning back as text.
13. **Cache breakpoints.** Add a 3rd, moving cache breakpoint on the last block of each request; OpenAI additionally sets `prompt_cache_key = session_id`.

Accepted 2026-09-27:

14. **Upload lifecycle in the projection.** An Upload stays inline for the next 3 user messages after it was attached; at the first user-message boundary after that (or at Compaction, if earlier) the projection swaps it for a stub note, never mid tool loop. The Event Log is unchanged. The model re-reads via the built-in tool `read_upload(id, pages?)`. A deleted Upload (`upload.deleted`) is swapped for a deleted stub at once. A 24 MB inline budget can swap the oldest inline Upload early, and `read_upload` results are stubbed the same way after 3 user messages, oldest first ([uploads-artifacts.md](uploads-artifacts.md)).
15. **Project files index** in the prompt (layer 3), not file contents; updates on change, accepted cache break. Replay uses the Project Files that existed at `turn.started` time ([uploads-artifacts.md](uploads-artifacts.md) Decision 24).
16. **Skill index and child prompt** ([agents-skills.md](agents-skills.md)): layer 3 carries each available Skill's name + description, never a body; a Child Session's prompt is built fresh from its own Agent, the Project instructions, the indexes and the task.

## 10. Edge cases

- **Model switch mid-session**: W changes; thresholds re-evaluate against the new model's window. Prior summaries are plain text and carry over.
- **Tier 1 frees < 15% of W**: skip Tier 1. Tier 2 runs only once T > 0.75·W (or on `/compact`).
- **Flush turn would overflow the window**: skip it, summarize immediately, `flush_skipped: true`.
- **Child result over its cap**: the full output is saved as an Artifact; the result carries the capped summary plus the Artifact link.
- **Single oversized turn**: hard-ceiling truncation in the projection only; the full event stays in the log.
- **Open Approval near the split point**: move the split so the Approval is not split.
- **Repeated Compaction**: the new summary takes the previous summary as input (chaining).
- **Crash mid-Compaction**: replay uses recorded events only; a Compaction without its event did not happen.
- **Incognito / Child Session reaching Tier 2**: no flush turn.
- **`/model` to a different provider**: the new model's projection drops all prior thinking blocks (keeps text and tool calls); Gemini gets a dummy signature on carried-over function calls.

## 11. Acceptance criteria

- After any Compaction, the Event Log's pre-existing events are byte-identical to before.
- Replaying a session's log on a fresh Worker produces a prompt identical to the one the original Worker sent.
- At T > 0.6·W with enough old results, a `context.cleared` event is appended, the last 5 tool results stay intact, `memory` and `delegate` results are never cleared, and cleared results show the placeholder.
- If clearing would free < 15% of W, no `context.cleared` event is written, and Tier 2 does not run while T ≤ 0.75·W (absent `/compact`).
- When the flush turn would overflow the window, no flush turn runs and the `context.compacted` event has `flush_skipped: true`; otherwise `flush_skipped: false`.
- After a Compaction, the chat shows the "Earlier messages summarized · view summary" divider, and all earlier messages remain scrollable.
- At T > 0.75·W (or `/compact`), exactly one flush turn precedes the `context.compacted` event (except incognito, Child Sessions, and `flush_skipped: true`); the last 4 turns remain verbatim; the summary contains all template sections.
- `context.compacted.model` equals the session's current model; its tokens appear in Usage.
- `/compact` produces `trigger=manual`; threshold produces `trigger=auto`.
- After Compaction, invoked Skill bodies (≤ 5k tokens each) and a refreshed memory index are in the prompt.
- The prompt prefix up to the first breakpoint is byte-stable across turns within a session (no index rewrite, no timestamps).
- A Child Session compacts on its own log; its result to the parent is ≤ 2,000 tokens by default (or the Agent's configured cap), and longer output is saved as an Artifact linked in the result.
- Switching Provider after a Compaction works: the summary is plain text in the neutral format.
- After a clear/summary or a `/model` switch to a different provider, the projection has no thinking block after the first edited spot (or from the old provider); tool calls and text remain, and Gemini receives a dummy signature on carried-over function calls.
- Every request carries a 3rd cache breakpoint on its last block; OpenAI requests also carry `prompt_cache_key = session_id`.

## 12. Research

Background, vendor comparisons and sources: [../research/context.md](../research/context.md).

## 13. Open gaps

None.
