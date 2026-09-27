# Context Management and Compaction: Research

> Background only. The build spec is [../design/context.md](../design/context.md); implementers read that, not this.

Status: research, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Every factual claim has an inline primary source. Anything I could not check is marked **unverified**.

Scope: **in-session** context management only: tool-result clearing, **Compaction**, and the memory flush turn. Cross-session memory research is in [memory.md](memory.md).

---

## 1. Problem in our runtime

| Constraint | Source | What it means for Compaction |
|---|---|---|
| The **Event Log** is the only source of truth for a Session | [ADR 0001](../adr/0001-own-durable-execution-on-postgres.md), CONTEXT.md | Compaction must never delete or rewrite events. The prompt sent to the model is a *projection* of the log. |
| Replay after a crash must reproduce state | ADR 0001 | Compaction and tool-result clearing must be recorded as events, so a new Worker builds the same prompt. They must not be recomputed differently on replay. |
| Neutral message format; model switch mid-session | [ADR 0002](../adr/0002-neutral-message-format.md) | Summaries must be plain text in our format. Opaque vendor artifacts (for example OpenAI's encrypted compaction item) can't carry over to another Provider. |
| BYOK | [ADR 0006](../adr/0006-byok-llm-platform-metered-services.md) | Compaction LLM calls spend the *user's* key, so they are Usage and count against the Budget. |
| **Child Sessions** are normal sessions ([ADR 0005](../adr/0005-agents-and-child-sessions-unified.md)) | ADR 0005 | Each Child Session has its own log, context window and Compaction. |

---

## 2. Taxonomy

| Technique | What it does | jelly-fish term |
|---|---|---|
| Tool-result clearing / context editing | Replace old tool results with a placeholder | Tier 1 of **Compaction** |
| Summarization | Replace old turns with an LLM summary | Tier 2 of **Compaction** |
| Thinking-block clearing | Drop old thinking blocks | Not used yet |
| Cross-session memory | Notes that outlive the Session | **Project Memory** / **User Memory**, see [memory.md](memory.md) |

---

## 3. What exists now

### Anthropic
- **Context editing** (beta `context-management-2025-06-27`) ([docs](https://platform.claude.com/docs/en/build-with-claude/context-editing)):
  - `clear_tool_uses_20250919`: `trigger` defaults to 100k input tokens; `keep` defaults to 3 tool uses. Also has `clear_at_least`, `exclude_tools`, and `clear_tool_inputs` (default false).
  - `clear_thinking_20251015`: controls how many thinking turns to keep.
  - Clearing invalidates the cached prefix. `clear_at_least` exists so each clear is large enough to be worth the cache rewrite.
  - Claude gets a warning before clearing so it can save to memory first.
- **Server-side compaction**. Two forms ([overview](https://platform.claude.com/docs/en/build-with-claude/compaction)):
  - *On demand*: beta `compact-2026-09-04`. It can keep recent turns word for word and can run in the background.
  - *Threshold*: `compact_20260112`, beta `compact-2026-01-12`. The trigger defaults to 150k tokens (minimum 50k). The API drops everything before the `compaction` block. It supports `instructions` and `pause_after_compaction`, and recommends putting `cache_control` on the compaction block ([docs](https://platform.claude.com/docs/en/build-with-claude/compaction-threshold)). The default prompt asks for "state, next steps, learnings" inside `<summary>` tags.
- **Prompt caching**: the cache is a strict prefix tools → system → messages; a change invalidates that level and everything after it. Up to 4 breakpoints. 5-minute writes cost 1.25x, 1-hour writes 2x, reads 0.1x (0.05x on Opus 5.5) ([caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)).
- **Engineering guidance**: Claude Code's compaction keeps "architectural decisions, unresolved bugs, and implementation details", drops "redundant tool outputs", and continues with "the five most recently accessed files". Tool-result clearing is the "lightest" form of compaction. Sub-agents return "1,000-2,000 token" summaries ([blog](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)).
- **Claude Code**: after `/compact`, the body of each invoked skill is re-injected (capped at 5,000 tokens per skill). The skill listing is not re-injected ([context window](https://code.claude.com/docs/en/context-window)). Subagents don't inherit the main memory ([subagents](https://code.claude.com/docs/en/sub-agents)).

### OpenAI
- **Agents SDK Sessions**: a `Session` protocol (`get_items/add_items/pop_item/clear_session`) with SQLite, Redis, SQLAlchemy, Mongo, Dapr, Conversations-API and `EncryptedSession` backends. `OpenAIResponsesCompactionSession` wraps any session and compacts through the Responses API, with `compaction_mode` set to `previous_response_id | input | auto` and a manual `run_compaction()` ([docs](https://openai.github.io/openai-agents-python/sessions/)).
- **Responses API compaction** ([docs](https://developers.openai.com/api/docs/guides/compaction)):
  - Server-side: `context_management` with a `compact_threshold`.
  - Standalone: `/responses/compact`.
  - Both return an **opaque, encrypted compaction item** that is "not intended to be human-interpretable", and you should pass the output back "as-is". This clashes with ADR 0002.

### Google Gemini
- It has server-side conversation state (`previous_interaction_id` in the Interactions API) ([docs](https://ai.google.dev/gemini-api/docs/interactions-overview)).
- It has implicit context caching on 2.5+ models, with minimum sizes of 2,048 or 4,096 tokens depending on model ([caching](https://ai.google.dev/gemini-api/docs/caching)).
- I found no API-level compaction feature. **Absence not proven.**

### Letta / MemGPT
- MemGPT treats the context window as an OS memory hierarchy ("virtual context management"), paging between tiers ([paper, 2023](https://arxiv.org/abs/2310.08560)).
- Evicted messages are "still retrievable via the API … and retrieval tools" ([docs](https://docs.letta.com/guides/agents/context-engineering)). This is the same idea as our Event Log.
- Sleep-time agents can run on compaction ([docs](https://docs.letta.com/guides/agents/architectures/sleeptime)).

### Comparison

| System | Trigger | Mechanism | Output | Provider-neutral | Full history kept |
|---|---|---|---|---|---|
| Anthropic context editing | 100k default | Clear tool results / thinking | Placeholders | No (API feature) | Client's job |
| Anthropic server compaction | 150k default (min 50k), or on demand | LLM summary, optional recent turns kept | Readable `<summary>` block | No (API feature) | Client's job |
| Claude Code `/compact` | `/compact` | Summary + re-injected skills and recent files | Readable | n/a | Local transcript |
| OpenAI Responses compaction | `compact_threshold`, or `/responses/compact` | Server | Opaque encrypted item | No | Client's job |
| OpenAI Agents SDK | Mode-dependent | Wraps Responses compaction | Opaque item | No | Session backend |
| Letta | Not documented here | Evict (virtual context) | Readable | Yes | Yes (retrievable) |
| Gemini API | None found | n/a | n/a | n/a | n/a |

---

## 4. Industry practice

| Aspect | Industry practice | Implication for us |
|---|---|---|
| Trigger | Anthropic threshold: 150k default, 50k minimum. Context editing: 100k. OpenAI: configurable `compact_threshold` (§3). | Trigger on a **fraction of the current model's window**, not a fixed number. Models differ, and the user can switch mid-session. |
| Cheapest step first | Tool-result clearing is the "lightest touch" ([blog](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)) | Tier 1: replace old tool *results* with a placeholder, keep the call inputs. Tier 2: LLM summary. |
| What's kept | System prompt; recent turns word for word ([keep-recent](https://platform.claude.com/docs/en/build-with-claude/compaction-keep-recent-turns)); decisions, open bugs and next steps; the 5 most recent files; invoked skills re-injected (≤5k tokens each) | Keep: system prompt, Agent instructions, memory index, last K turns, invoked Skill bodies, a structured summary. |
| Summary vs clear | Clearing is deterministic and cheap but loses content. Summarizing costs an LLM call and loses detail. | Both, tiered. The full content always stays in the Event Log. |
| Log vs view | Letta: evicted messages stay retrievable. Anthropic: the API drops content before the compaction block, but your client keeps the full history. | The Event Log is untouched. A `compaction` event records `(covers_through_seq, summary)`. The prompt is a projection. |
| Opaque vs readable | OpenAI's item is encrypted and opaque | Our own plaintext summaries, so the session stays provider-neutral (ADR 0002). |
| Prompt caching | Strict prefix; clearing invalidates the cache, hence `clear_at_least` ([caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)) | Compact **rarely and in big chunks**. Keep tools and system byte-stable. Breakpoints at the end of the memory index and on the compaction summary. No timestamps or other volatile data before a breakpoint. |
| Pre-compaction save | Claude is warned before clearing so it can save to memory ([context editing](https://platform.claude.com/docs/en/build-with-claude/context-editing)). Letta's sleep-time agent can run on compaction. | One memory flush turn before Tier 2 (see spec, [../design/context.md](../design/context.md)). |
| Child sessions | Sub-agents return 1-2k-token summaries. Claude Code subagents don't inherit main memory. | A Child Session compacts independently. The parent sees only the result the child returns. |

---

## 5. Conclusion

Our own two-tier Compaction, stored as events. The Event Log is never modified; the prompt is a projection. No vendor server-side compaction: it breaks model switching (ADR 0002), and OpenAI's is opaque. Thresholds in the spec are starting values to tune with the eval suite, not researched values.

Full spec and decisions: [../design/context.md](../design/context.md).

---

## 6. Sources
Anthropic
- Context editing: https://platform.claude.com/docs/en/build-with-claude/context-editing
- Compaction overview: https://platform.claude.com/docs/en/build-with-claude/compaction
- Compaction at a threshold: https://platform.claude.com/docs/en/build-with-claude/compaction-threshold
- Compaction keeping recent turns: https://platform.claude.com/docs/en/build-with-claude/compaction-keep-recent-turns (linked from overview; not fetched separately)
- Prompt caching: https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- Effective context engineering: https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- Claude Code context window: https://code.claude.com/docs/en/context-window

OpenAI
- Agents SDK sessions: https://openai.github.io/openai-agents-python/sessions/
- Responses API compaction: https://developers.openai.com/api/docs/guides/compaction

Google
- Interactions API: https://ai.google.dev/gemini-api/docs/interactions-overview
- Context caching: https://ai.google.dev/gemini-api/docs/caching

Frameworks and papers
- MemGPT: https://arxiv.org/abs/2310.08560
- Letta context/compaction: https://docs.letta.com/guides/agents/context-engineering
- Letta sleep-time agents: https://docs.letta.com/guides/agents/architectures/sleeptime
