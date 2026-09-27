# Agent Loop: Research

> Background only. Durable execution (fold data + pure `Decide`, Leases, Worker, waiting states, Steering, Interrupt, Budget checks) is specified in [../design/event-log.md](../design/event-log.md) and is not redesigned here. This doc covers the loop that runs *inside* one `exec(step)`: building the prompt, calling the Provider, running Tools, and what gets appended when.

Status: research, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Facts cite primary sources inline (docs, SDK source on `main` as of 2026-09-24, first-party blogs). **Unverified** marks anything not confirmed.

---

## 1. Where the loop sits

- The event-log `Decide(state)` returns one step: `StartTurn`, `RequestTool`, `AskApproval`, `StartTool`, `MarkInterrupted`, `InjectSteering`, `Park`, `Complete`. `exec` performs it and appends events ([event-log.md §5.3](../design/event-log.md)).
- "The agent loop" in other harnesses is a `while` loop: call the model, run the tools, repeat. For us it is **the sequence of steps `Decide` produces**, and each step is short and logged. The research question is what each step does and which rules it must obey.
- One jelly-fish **Turn** = one LLM call plus the tool calls it requests (CONTEXT.md). Anthropic calls the whole tool-use loop until the next real user message "one assistant turn" ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)). That matters for thinking blocks (§3.5).

---

## 2. How leading harnesses structure the loop

- **Claude Agent SDK / Claude Code.**
  - The loop: prompt, then Claude evaluates, then the SDK runs tools and feeds the results back. It repeats until "a response with no tool calls". "Each full cycle is one turn" ([agent loop](https://code.claude.com/docs/en/agent-sdk/agent-loop)).
  - Limits: `max_turns` ("counts tool-use turns only") and `max_budget_usd` both default to no limit. The ResultMessage subtypes are `success`, `error_max_turns`, `error_max_budget_usd` and `error_during_execution`. Each carries `stop_reason`, `num_turns` and `total_cost_usd` (same page).
  - Parallel tools: read-only tools (Read/Glob/Grep, MCP tools with `readOnlyHint`) run concurrently. Edit/Write/Bash run sequentially, and custom tools are sequential by default (same page).
  - Permission order: hooks, then deny rules, then ask rules, then the permission mode, then allow rules, then `canUseTool`. "A hook deny applies even in `bypassPermissions` mode" ([permissions](https://code.claude.com/docs/en/agent-sdk/permissions)).
- **Anthropic "Building effective agents".**
  - Distinguishes workflows ("predefined code paths") from agents, which "dynamically direct their own processes".
  - Agents need "ground truth from the environment at each step" and "stopping conditions (such as a maximum number of iterations)".
  - Invest in the agent–computer interface: poka-yoke tools, formats that are natural for the model, and simplicity ([post](https://www.anthropic.com/engineering/building-effective-agents)).
- **Anthropic context engineering and tools posts.**
  - Aim for "the smallest possible set of high-signal tokens". Tools should be "self-contained, robust to error". Retrieve just in time via identifiers ([context engineering](https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents)).
  - "More tools don't always lead to better outcomes". Namespace tool names. Write helpful error messages. Claude Code caps tool output at 25k tokens by default ([writing tools](https://www.anthropic.com/engineering/writing-tools-for-agents)).
  - Multi-agent: "agents are stateful and errors compound", so resume from the failure point ([research system](https://www.anthropic.com/engineering/multi-agent-research-system)).
- **OpenAI Agents SDK.**
  - Runner loop: call the LLM, then classify the output. Final output ends the run; a handoff switches agent and loops; tool calls run and loop. Past `max_turns` it raises `MaxTurnsExceeded` ([running agents](https://openai.github.io/openai-agents-python/running_agents/)). `DEFAULT_MAX_TURNS = 10` ([run_config.py](https://github.com/openai/openai-agents-python/blob/main/src/agents/run_config.py)).
  - Handoffs are tools named `transfer_to_<agent>`, with an `input_filter` over the history ([handoffs](https://openai.github.io/openai-agents-python/handoffs/)).
  - Guardrails ([guardrails](https://openai.github.io/openai-agents-python/guardrails/), [ref](https://openai.github.io/openai-agents-python/ref/guardrail/)):
    - Input guardrails run only on the first agent's input. They run in parallel with the agent by default (`run_in_parallel=True`), so tokens can be spent before the tripwire fires.
    - Output guardrails run on the final output.
    - Tool guardrails run before and after each guarded call.
  - `tool_use_behavior`: `run_llm_again` (default), `stop_on_first_tool`, `StopAtTools`, or a custom function. `tool_choice` resets to auto after a tool call "to prevent infinite loops" ([agents](https://openai.github.io/openai-agents-python/agents/)).
  - Tools ([tools](https://openai.github.io/openai-agents-python/tools/), [ref](https://openai.github.io/openai-agents-python/ref/tool/)):
    - Errors go back to the model via `failure_error_function`. The default text is "An error occurred while running the tool. Please try again."
    - Per-tool `timeout_seconds`, with `timeout_behavior="error_as_result"` or `"raise_exception"`.
    - `needs_approval` pauses the run, and `result.interruptions` → `state.approve()` resumes it ([HITL](https://openai.github.io/openai-agents-python/human_in_the_loop/)).
  - Parallel: one `asyncio` task per function call, with an optional `max_function_tool_concurrency` ([tool_execution.py](https://github.com/openai/openai-agents-python/tree/main/src/agents/run_internal)).
- **Codex CLI** (Rust, open source).
  - `run_turn` loops over sampling requests. Each iteration drains `input_queue.get_pending_input` (a steered user message), and `needs_follow_up = model_needs_follow_up || has_pending_input` ([turn.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/session/turn.rs)).
  - Parallel tools: parallel-capable tools share a read lock; the rest take a write lock and run exclusively ([parallel.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/parallel.rs)).
  - Retries: `DEFAULT_STREAM_MAX_RETRIES = 5`, `DEFAULT_REQUEST_MAX_RETRIES = 4`, and a 300 s stream idle timeout ([model-provider-info](https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs)). Exec default timeout is 10 s, with exit code 124 on timeout ([exec.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/exec.rs)).
  - Auto-compact happens inside the turn loop at `min(limit, 0.9·window)` ([openai_models.rs](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/openai_models.rs)).
- **Gemini CLI** (TS, open source).
  - `MAX_TURNS = 100` per prompt. `processTurn`, in order ([client.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/client.ts)):
    1. Check the session turn count.
    2. Try to compress the chat.
    3. Check for context overflow.
    4. Loop detection.
    5. Pick the model via the router.
    6. `turn.run()`.
  - A "next speaker" LLM check can auto-send "Please continue." (same file).
  - Loop detection flags 5 identical consecutive tool calls, or content chanting, or an LLM check after 30 turns ([loopDetectionService.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/services/loopDetectionService.ts)).
  - Scheduler states: validating, scheduled, awaiting_approval, executing, success, error, cancelled. Contiguous parallelizable calls are batched with `Promise.all`; edits run sequentially ([scheduler.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/scheduler/scheduler.ts)).
- **LangGraph / LangChain v1.**
  - `create_agent` builds a `"model"` node, a `"tools"` node and conditional edges, with middleware nodes around them ([factory.py](https://raw.githubusercontent.com/langchain-ai/langchain/master/libs/langchain_v1/langchain/agents/factory.py)).
  - `recursion_limit` defaults to 1000 super-steps and raises `GraphRecursionError` ([graph API](https://docs.langchain.com/oss/python/langgraph/graph-api)).
  - `interrupt()` + `Command(resume=…)`: "the runtime restarts the entire node from the beginning", so side effects before an interrupt should be idempotent ([interrupts](https://docs.langchain.com/oss/python/langgraph/interrupts)). Our fold-data approach avoids re-running code.
  - `ToolNode` runs calls in parallel. By default only argument-validation errors come back to the model; other exceptions propagate ([tool_node.py](https://raw.githubusercontent.com/langchain-ai/langgraph/main/libs/prebuilt/langgraph/prebuilt/tool_node.py)).
- **Letta** (source at tag 0.16.8; the repo has moved to letta-code).
  - `letta_v1_agent` has "no heartbeats (loops happen on tool calls)". The rule is: no tool call ends the loop, and any tool call (even a failed one) continues it ([letta_agent_v3.py](https://github.com/letta-ai/letta/blob/0.16.8/letta/agents/letta_agent_v3.py)).
  - Stop reasons: `end_turn`, `max_steps`, `max_tokens_exceeded`, `tool_rule`, `requires_approval`, `cancelled`, `llm_api_error`… ([letta_stop_reason.py](https://github.com/letta-ai/letta/blob/0.16.8/letta/schemas/letta_stop_reason.py)).
  - `DEFAULT_MAX_STEPS = 50` ([constants.py](https://github.com/letta-ai/letta/blob/0.16.8/letta/constants.py)).
  - Tool rules include Terminal, Init, Child and RequiredBeforeExit ([tool_rule.py](https://github.com/letta-ai/letta/blob/0.16.8/letta/schemas/tool_rule.py)).
- **Pydantic AI.**
  - The run is a graph: `UserPromptNode → ModelRequestNode → CallToolsNode → End`, stepped with `agent.iter()` ([_agent_graph.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/_agent_graph.py)).
  - `UsageLimits.request_limit = 50` by default, plus token, tool-call and `cost_limit` limits; exceeding one raises `UsageLimitExceeded` ([usage.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/usage.py)).
  - `ModelRetry` sends a retry prompt back to the model; per-tool retries default to 1. `tool_timeout` counts as a retryable failure ([tools advanced](https://pydantic.dev/docs/ai/tools-toolsets/tools-advanced/)).
  - `end_strategy` is `graceful` (default), `early` or `exhaustive`. `FallbackModel(default, *fallbacks, fallback_on=(ModelAPIError,))` ([fallback.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/models/fallback.py)).
- **Vercel AI SDK** (v7 is current).
  - `generateText`/`streamText` default to `stopWhen = isStepCount(1)` ([generate-text.ts](https://github.com/vercel/ai/blob/main/packages/ai/src/generate-text/generate-text.ts)). `ToolLoopAgent` defaults to `isStepCount(20)` ([loop control](https://github.com/vercel/ai/blob/main/content/docs/03-agents/04-loop-control.mdx)).
  - `maxSteps` was replaced by `stopWhen` in v5 ([v5 guide](https://github.com/vercel/ai/blob/main/content/docs/08-migration-guides/26-migration-guide-5-0.mdx)). `prepareStep` can change model, tools and messages per step.
  - Tools run via `Promise.all`. Approval does not pause: the call returns `tool-approval-request` parts and the caller calls again ([tools doc](https://github.com/vercel/ai/blob/main/content/docs/03-ai-sdk-core/15-tools-and-tool-calling.mdx)).

### Comparison

| Harness | Loop shape | Default turn cap | Parallel tools | Tool error → model | Approval pause | Durable? |
|---|---|---|---|---|---|---|
| Claude Agent SDK | while no-tool-calls | none (`max_turns`, `max_budget_usd`) | read-only concurrent, writes sequential | yes (rejection text) | `canUseTool`/hooks, in-process | JSONL resume |
| OpenAI Agents SDK | Runner classify-and-loop | 10 | all, optional cap | yes (`failure_error_function`) | `needs_approval` → RunState | via Temporal |
| Codex CLI | `run_turn` sampling loop | none found (**unverified**) | read-lock/write-lock | yes | approval policy | rollout file |
| Gemini CLI | recursive `sendMessageStream` | 100/prompt; session −1 | contiguous parallel-safe batch | yes | scheduler `awaiting_approval` | chat files (**unverified**) |
| LangGraph | model⇄tools graph | 1000 super-steps | all | arg errors only (default) | `interrupt()` re-runs node | checkpointer |
| Letta v1 | loop while tool called | 50 steps | only without tool rules | yes | `requires_approval` stop | server DB |
| Pydantic AI | node graph | 50 requests | all (`asyncio`) | `ModelRetry` | deferred tools → new run | message history |
| Vercel AI SDK | step loop | 1 (fn) / 20 (agent) | all | yes (`tool-error`) | returns, caller re-calls | none |
| **jelly-fish** | `Decide` steps over the log | Budget (turns) | parallel-safe only (proposal) | yes, `is_error` | park `awaiting_approval` | Event Log |

Lessons: every harness (1) treats "no tool calls" as done, (2) feeds tool errors back as data, (3) separates read-only from mutating tools for concurrency, and (4) needs a hard cap. Only Letta and ours make approvals a durable stop without holding a process.

---

## 3. Loop mechanics

### 3.1 Stop conditions

| Signal | Anthropic | OpenAI Responses | Gemini | Proposed handling |
|---|---|---|---|---|
| Done | `end_turn` | completed, no function_call | `STOP` | pending Steering → `StartTurn`; else `Park(awaiting_user)` |
| Tools | `tool_use` | `function_call` items | functionCall parts | `RequestTool` × N |
| Truncated | `max_tokens` | `incomplete` + `max_output_tokens` | `MAX_TOKENS` | see below |
| Refusal | `refusal` (+`stop_details`) | `refusal` content part | `SAFETY`/`PROHIBITED_CONTENT`… | record, `Park(awaiting_user)`, no auto-retry |
| Server-tool pause | `pause_turn` (send back as-is) | — | — | N/A (our tools are client-side) |
| Window full | `model_context_window_exceeded` | **unverified** | `ContextWindowWillOverflow` (CLI-side) | force Compaction Tier 2, then retry the turn |
| Malformed call | — | — | `MALFORMED_FUNCTION_CALL`, `MISSING_THOUGHT_SIGNATURE` | retry once, then `session.error` |

Sources: [Anthropic stop reasons](https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons), [OpenAI reasoning](https://developers.openai.com/api/docs/guides/reasoning), [Gemini FinishReason](https://ai.google.dev/api/generate-content).

- **`max_tokens`**: if the response has an incomplete tool_use block, Anthropic says you'll "need to retry the request with a higher `max_tokens`". On 4.6+ models, continue with a user message rather than an assistant prefill ([stop reasons](https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons), [streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)). OpenAI can hit `max_output_tokens` before any visible output, while still billing the reasoning ([reasoning](https://developers.openai.com/api/docs/guides/reasoning)).
  - Proposal: drop any incomplete tool_use. Re-run the turn once with `max_tokens` raised to the model's max. If it is still truncated, record the text and `Park(awaiting_user)`.
- **Empty `end_turn` after tool results**: Anthropic says to add a continuation user message rather than retrying as-is ([stop reasons](https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons)). Gemini CLI appends a nudge on an empty response ([geminiChat.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/geminiChat.ts)).
- **Turn/time/$ caps** are our **Budget** (event-log §6 Q15), so there is no separate `max_turns` concept.

### 3.2 Parallel tool calls

- Anthropic: return "one `tool_result` for each `tool_use` block, all together in the next user message". Tool results come "FIRST in the content array. Any text must come AFTER" ([parallel](https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use), [handle calls](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)). Parallel can be disabled with `tool_choice.disable_parallel_tool_use`.
- OpenAI: parallel by default; `parallel_tool_calls:false` gives zero or one call ([function calling](https://developers.openai.com/api/docs/guides/function-calling)).
- Gemini: send responses "in the same order as they were requested", matched by id. "FC1+sig, FC2, FR1, FR2" is valid; interleaving FC1, FR1, FC2 returns a 400 ([thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures), [function calling](https://ai.google.dev/gemini-api/docs/generate-content/function-calling)).
- **Ordering rule for us**: log order = completion order (wall-clock truth). The projection re-sorts results into tool_use order and emits them as one block. That makes the log a record of time and the prompt a pure function of it.
- **Partial failure**: one failing tool is just an `is_error` result. Siblings keep running. **Don't use `errgroup.WithContext` for tools**: its ctx "is canceled the first time a function passed to Go returns a non-nil error" ([errgroup](https://pkg.go.dev/golang.org/x/sync/errgroup)), which would kill healthy siblings. Either have goroutines always return nil and put the result in a slot, or use `sync.WaitGroup` plus a semaphore (`errgroup.SetLimit` is fine without `WithContext`).
- Concurrency policy: Claude Code and Codex both run only read-only or parallel-safe tools concurrently. Shell/file mutation in one Sandbox is inherently ordered.

### 3.3 Tool errors, retries, timeouts

- Anthropic `is_error: true` with instructive text (e.g. "Rate limit exceeded. Retry after 60 seconds.") ([handle calls](https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls)).
- Harnesses split two ways:
  - Always feed errors back to the model: Claude, OpenAI, Vercel, Letta.
  - Raise some errors instead: LangGraph by default, and OpenAI with `failure_error_function=None`.
  - Pydantic AI adds bounded model-driven retries (`ModelRetry`, default 1).
  - LangChain `ToolRetryMiddleware` retries the *tool* itself: `max_retries=2`, backoff ×2 ([built-in middleware](https://docs.langchain.com/oss/python/langchain/middleware/built-in)).
- For us (ADR 0001): **never auto-retry a started tool call**. Every failure (exception, timeout, denied, bad JSON args, unknown tool) becomes `tool.call.completed{is_error:true}` with a short, model-readable message. Retry is the model's decision.
- Timeouts per tool: OpenAI `timeout_seconds` + `error_as_result`; Codex exec 10 s default; Gemini hooks 60 s. Proposal: `ToolDef.Timeout` default 120 s, shell up to the Sandbox wall-clock cap. On timeout the result reads "timed out after Ns; the command may have partially run".

### 3.4 Streaming tool-call arguments

- Anthropic `input_json_delta.partial_json`. With `eager_input_streaming: true` "you might receive partial or invalid JSON", and `max_tokens` can cut a parameter off ([fine-grained streaming](https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming)).
- OpenAI `response.function_call_arguments.delta` / `.done` ([function calling](https://developers.openai.com/api/docs/guides/function-calling)).
- Gemini `streamFunctionCallArguments` is "not supported in Gemini API" (Vertex only) ([go-genai types.go](https://github.com/googleapis/go-genai/blob/main/types.go)).
- Rule: deltas go to the UI only (ephemeral, via `DeltaBus`, event-log §3.3). **Execute only from the final accumulated args**. If they don't parse or fail validation, return `is_error` "invalid arguments: …" (like Vercel's `InvalidToolInputError` and LangGraph's `ToolInvocationError`).

### 3.5 Thinking / reasoning blocks

- **Anthropic** ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking), [preserved thinking](https://platform.claude.com/docs/en/build-with-claude/preserved-thinking)):
  - Every block carries an opaque `signature`. Within a tool-use turn, you must pass thinking blocks back "complete and unmodified". `redacted_thinking` has to be kept too.
  - Cross-model: a block is readable only by certain models; unreadable ones are dropped silently.
  - **New (Fable 5.1 / Opus 5.5): prefix binding.** "A block stays valid only while the top-level `system` prompt, the `tools`, and the messages before it are unchanged". This is enforced by default for accounts created on or after 2026-08-31. What breaks it: clearing or shortening an earlier `tool_result`, editing `tools`, or keep-tail compaction. What doesn't: removing thinking blocks "from start, end, or all". There is an escape hatch, beta `thinking-binding-controls-2026-08-01` with `prefix_mismatch_behavior:"drop_block"`.
  - ⚠ Our Tier 1 clearing and Tier 2 keep-last-K ([context.md §5.3](../design/context.md)) are exactly these edits. See Q5.
- **OpenAI**: with `store:false`, request `include:["reasoning.encrypted_content"]`. Reasoning items "must also be passed back with tool call outputs" ([reasoning](https://developers.openai.com/api/docs/guides/reasoning), [function calling](https://developers.openai.com/api/docs/guides/function-calling)).
- **Gemini 3**: a `thoughtSignature` sits on the first functionCall of each step and is strictly validated within the current turn. For injected or foreign history, send the dummy `"skip_thought_signature_validator"` ([thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)). Gemini CLI does this in `ensureActiveLoopHasThoughtSignatures` ([geminiChat.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/geminiChat.ts)).
- **Cross-provider precedent**:
  - Pydantic AI sends a native thinking block only when `provider_name` matches and a signature exists; otherwise it sends the thinking as tagged text ([anthropic.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/models/anthropic.py)).
  - Vercel drops reasoning without an Anthropic signature ([convert-to-anthropic-prompt.ts](https://github.com/vercel/ai/blob/main/packages/anthropic/src/convert-to-anthropic-prompt.ts)).
- **For us (ADR 0002)**: the neutral `ThinkingPart{text, provider, model, opaque []byte}` stores the signature or encrypted blob as bytes the core never interprets. The adapter replays it only when `provider` matches. For foreign history it drops the thinking, and on Gemini it adds the dummy signature to foreign function calls.

### 3.6 Provider errors

- **Anthropic** ([errors](https://platform.claude.com/docs/en/api/errors)):
  - 429 `rate_limit_error`. A spend-cap 429 has no `retry-after` and "keeps failing".
  - 529 `overloaded_error`; 500 `api_error`.
  - SSE errors can arrive after a 200. Every response has a `request-id` header.
- **SDK retries**:
  - anthropic-sdk-go and openai-go both default `MaxRetries: 2`. They honor `Retry-After(-Ms)`; otherwise backoff is 0.5 s·2ⁿ, capped at 8 s, with jitter ([anthropic requestconfig.go](https://github.com/anthropics/anthropic-sdk-go/blob/main/internal/requestconfig/requestconfig.go), [openai requestconfig.go](https://github.com/openai/openai-go/blob/main/internal/requestconfig/requestconfig.go)).
  - These retries cover only the initial HTTP response. A mid-stream failure is not retried (**inferred from code, unverified in docs**). "Tool use and extended thinking blocks cannot be partially recovered" ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)).
- **Gemini CLI** ([retry.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/utils/retry.ts), [fallback/handler.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/fallback/handler.ts)):
  - 10 attempts, 5–30 s exponential backoff. Delays come from `RetryInfo`; one over 300 s is terminal.
  - A persistent 429 calls `handleFallback`, which switches to another model via a policy chain, usually asking the user.
- **Model fallback**: Pydantic `FallbackModel` and LangChain `ModelFallbackMiddleware` switch silently. Anthropic suggests a fallback model for `refusal`.
  - Under BYOK, a silent switch spends the user's money on a model they didn't pick, and may need a different Provider Key. Proposal: no automatic fallback in the base version.
- **Proposal**: set SDK retries to 0 and own a turn-level policy:

| Class | Examples | Action |
|---|---|---|
| Transient | 429 with retry-after ≤ 60 s, 529, 500, 504, stream drop | in-process backoff, up to 4 attempts (like Codex), honor retry-after, respect ctx |
| Long wait | retry-after > 60 s, repeated 429s | `timer.set{wake_at}` → `sleeping` (frees the Worker) |
| Terminal | 401/403 (bad key), 402 billing, spend-cap 429, 400 invalid | `session.error{retryable:false}` → `awaiting_user`, UI suggests fixing the key or `/model` |
| Window | `model_context_window_exceeded`, 413 | Compaction Tier 2, then retry once |

  Each failed attempt that reported usage appends `usage.recorded`. A re-run turn is covered by event-log §3.6 (LLM calls are safe to repeat).

### 3.7 Prompt caching within the loop

- **Anthropic** ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching), [tool search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)):
  - Prefix order is `tools → system → messages`. "Changes at each level invalidate that level and all subsequent levels."
  - Up to 4 breakpoints. Lookback is 20 blocks per breakpoint, and a run of consecutive tool_result blocks counts as one position.
  - TTL is 5 min (refreshed on hit) or 1 h at 2× the write price. Minimum 512–4,096 tokens depending on model.
  - Changing tool definitions invalidates everything. `tool_choice` or images invalidate messages only.
  - A cache entry is readable only after the first response begins.
  - Tool search with `defer_loading` keeps deferred tools out of the prefix, so "prompt caching is preserved". Selection accuracy "degrades once you exceed 30–50 available tools".
- **OpenAI**: automatic, with a ≥1,024-token prefix and `prompt_cache_key`. Changing tools or their order breaks prefix matching, so "Keep tools consistent" and use `allowed_tools` to restrict ([prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)).
- **Gemini**: implicit caching for 2.5+, minimum 4,096 tokens on 3.x; put the common prefix first ([caching](https://ai.google.dev/gemini-api/docs/generate-content/caching)).
- **For us**: context.md §5.2 places 2 breakpoints (after the memory index, after the summary). Add a **3rd, moving breakpoint on the last block of each request**, so loop iteration n+1 reads iteration n's prefix. That gives 3 of 4 slots. Send `prompt_cache_key = session_id` to OpenAI. Parallel child sessions can't share a cache until the first response lands (fine).

---

## 4. Steering and Interrupt in practice

| | Mid-run user message | Esc / interrupt | What history gets after an interrupt |
|---|---|---|---|
| Claude Code | queued; "If Claude is running tool calls, it reads the message as soon as those calls finish, within the same turn" ([how it works](https://code.claude.com/docs/en/how-claude-code-works)); Esc keeps the queue and sends it ([interactive mode](https://code.claude.com/docs/en/interactive-mode#queue-messages-while-claude-works)) | cancels the running tool | user text `[Request interrupted by user]` (and `…for tool use`). The string is verified in [SDK sessions.py](https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/_internal/sessions.py) but undocumented. The rejection text "The user doesn't want to proceed with this tool use" is **unverified** (issue reports only) |
| Codex | `steer_input` → pending input drained at each loop iteration, recorded as `SteeredUserInput`; keeps the turn going ([turn_input.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/session/turn_input.rs)) | `Op::Interrupt`: 100 ms grace, abort the task, `TurnAborted` event ([tasks/mod.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tasks/mod.rs)) | synthetic function output `"aborted by user after {secs}s"` ([parallel.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/parallel.rs)), plus a user-role `<turn_aborted>` marker: "…If any tools/commands were aborted, they may have partially executed." ([turn_aborted.rs](https://github.com/openai/codex/blob/main/codex-rs/core/src/context/turn_aborted.rs)) |
| Gemini CLI | queued, auto-submitted when idle ([useMessageQueue.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/cli/src/ui/hooks/useMessageQueue.ts)); plus a "User steering update" hint placed before the tool responses | `AbortController.abort()` + `cancelAllToolCalls` ([useGeminiStream.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/cli/src/ui/hooks/useGeminiStream.ts)) | cancelled calls get `functionResponse{error:"[Operation Cancelled] Reason: …"}` ([state-manager.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/scheduler/state-manager.ts)). If *all* calls were cancelled, history is truncated back to before the model call |
| Agent SDK | streaming input: "Queued messages… with ability to interrupt" ([streaming modes](https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode)) | `interrupt()` returns a receipt listing pending messages | — |

What to write for us, given event-log §3.8 (`user.interrupt` → `CancelFunc`):

1. **Mid-LLM stream**: append `turn.interrupted{user_interrupt, partial_text}`. The projection *omits* the partial assistant output (no unmatched tool_use can reach the Provider). It adds a user-role marker `[Previous turn interrupted by user]` merged into the next user message (the Codex approach).
2. **Tools started, not finished**: `tool.call.interrupted{user_interrupt}` for each. The projection renders it as `is_error` "interrupted by user after Ns; it may have partially run" (Codex wording).
3. **Tools requested, not started**: `tool.call.completed{is_error, "not run: interrupted by user"}`. Every provider requires one result per call.
4. Then `Park(awaiting_user)`. Queued Steering is *not* auto-sent after an Interrupt, unlike Claude Code (see Q10).

- **Steering placement**: at the tool-result boundary the projection emits `[tool_result…, then steering text]` in **one** user message. That satisfies Anthropic "results first, text after" and Gemini's FC/FR ordering.
- **Steering after `end_turn`**: if a `user.message` arrived during the final LLM call, `Decide` returns `StartTurn` instead of parking (Codex `needs_follow_up = … || has_pending_input`).

---

## 5. Hooks and middleware

- **Claude Code hooks** ([hooks](https://code.claude.com/docs/en/hooks), [SDK hooks](https://code.claude.com/docs/en/agent-sdk/hooks)):
  - Events include PreToolUse, PostToolUse, PostToolUseFailure, PostToolBatch, UserPromptSubmit, Stop, SubagentStop, PreCompact and PermissionRequest.
  - Exit 2 = block. PreToolUse returns `permissionDecision` `allow|deny|ask|defer` and `updatedInput` (which "Replaces the entire input object"). PostToolUse can add `additionalContext`.
  - A Stop hook can `decision:"block"` to keep going. It is capped: "ends the turn after 8 consecutive blocks". `continue:false` stops everything.
- **Codex hooks**: PreToolUse, PermissionRequest, PostToolUse, Pre/PostCompact, UserPromptSubmit, Stop, Interrupt… ([hook.rs](https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/src/protocol/v2/hook.rs)).
- **Gemini CLI hooks**: BeforeTool, AfterTool, BeforeModel (can return a synthetic response), AfterModel (per chunk), BeforeToolSelection… ([hooks reference](https://github.com/google-gemini/gemini-cli/blob/main/docs/hooks/reference.md)).
- **OpenAI Agents SDK**:
  - Guardrails (tripwire exceptions).
  - `RunHooks`: `on_llm_start/end`, `on_tool_start/end`, `on_handoff` ([lifecycle](https://openai.github.io/openai-agents-python/ref/lifecycle/)).
- **LangChain v1 middleware** ([custom middleware](https://docs.langchain.com/oss/python/langchain/middleware/custom)):
  - Node hooks `before_agent`, `before_model`, `after_model`, `after_agent`. Wrap hooks `wrap_model_call`, `wrap_tool_call`, nested with the first middleware outermost.
  - `jump_to` accepts `end|tools|model`.
  - Built-ins: HITL, Summarization, ModelFallback, Model/ToolCallLimit, Tool/ModelRetry and PII.

Two shapes exist: **observer hooks** at fixed points (Claude, Gemini, OpenAI) and **wrapping middleware** (LangChain, `func(next) next`). Wrapping composes retries and fallback nicely, but it can do hidden work between two log appends.

**Rule for us: a hook never writes to the DB and never keeps state.** It returns a verdict plus events. `exec` appends them, and `Decide` reads them back from the log. Anything that isn't in the log didn't happen, so replay stays pure.

| Concern | Where it plugs in | Pure (`Decide`) or effectful (`exec` hook) | Events |
|---|---|---|---|
| Budget | before `StartTurn`/`StartTool` from projected counters; tokens after `llm.response` | pure | `budget.exceeded`, `approval.requested{kind:budget}` |
| Approver (rules → Jev → small LLM) | `BeforeTool` on the `RequestTool` step | effectful (Jev is a network call) | allow: trace on `tool.call.started` (Q3); ask: `approval.requested`; deny: `tool.call.completed{is_error, denied}`; `usage.recorded{jev}` |
| Taint ([memory.md §5.2](../design/memory.md)) | fold computes `UntrustedSinceUserMsg`; the `memory` tool reads it | pure | none extra (the memory tool's own `memory.written`) |
| Usage metering | in the same `Append` as `llm.response`; tool handlers for search/compute | effectful | `usage.recorded` |
| Compaction trigger | before `StartTurn` from a token estimate | pure | `context.cleared`, `compaction` |
| Large results | `AfterTool` → blob storage | effectful | `blob_ref` inside `tool.call.completed` |
| Tracing (OTel) | a decorator around `Provider`/`Tool`, not a hook | — | none |

```go
// package loop
type Hook interface {
    BeforeModel(ctx context.Context, st *State, req *llm.Request) (Verdict, error)
    AfterModel(ctx context.Context, st *State, resp *llm.Response) (Verdict, error)
    BeforeTool(ctx context.Context, st *State, call msg.ToolCall) (ToolVerdict, error)
    AfterTool(ctx context.Context, st *State, call msg.ToolCall, res *tool.Result) (Verdict, error)
}
type Verdict struct {
    Events []eventlog.NewEvent // appended by exec in the same tx as the step's own events
    Park   *Park               // non-nil: stop and set status (e.g. awaiting_approval)
}
type ToolVerdict struct {
    Verdict
    Decision Decision // Allow | Ask | Deny
    By       string   // "rule:<id>" | "jev" | "llm" | "mode:full-auto"
    Reason   string   // shown to the model on Deny
}
type NopHook struct{} // embed to implement only the methods you need
```

Hooks run in registration order. For `BeforeTool` the first non-Allow wins (like Claude's "deny beats allow"). No `UpdatedInput` in the base version, since rewriting arguments makes the audit trail harder to read.

---

## 6. System prompt assembly and tool definitions

Claude Code/SDK, Codex and Gemini all put stable content first and volatile content last. Gemini CLI injects IDE context only when no functionCall is pending ([client.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/client.ts)), and Letta renders memory blocks into the system prompt ([memory.py](https://github.com/letta-ai/letta/blob/0.16.8/letta/schemas/memory.py)). Proposed order, extending context.md §5.2:

| # | Block | Changes when | Cache |
|---|---|---|---|
| 1 | tools (sorted by name, deterministic JSON) | tool set changes (Q4) | prefix |
| 2 | platform prompt (safety, tool etiquette, Sandbox rules) | deploy | prefix |
| 3 | Agent instructions | Agent edit (snapshot at `session.created`) | prefix |
| 4 | Project instructions | session start / after Compaction | prefix |
| 5 | Skills index (`name — one-line description`) | session start / after Compaction | prefix |
| 6 | `<user_memory>` + `<project_memory>` index | session start / after Compaction | **BP1** |
| 7 | latest Compaction summary | Compaction | **BP2** |
| 8 | live turns; invoked Skill bodies arrive as tool results | every turn | **BP3** (moving) |
| 9 | volatile context (date, Sandbox state), in the newest user message only | every turn | tail |

- Skill bodies load through a tool call, so they land in the tail and never edit the prefix.
- Anthropic's mid-conversation `system` messages (Opus 5.5+) are an alternative for per-turn reminders ([thinking](https://platform.claude.com/docs/en/build-with-claude/thinking)). They are Anthropic-only, so neutral text in the user message is simpler.
- **Dynamic tools (MCP)**: a Connector attached mid-session, or `tools/list_changed`, alters block 1. That invalidates *all* Anthropic caching and, with prefix binding, every later thinking block. OpenAI and Gemini also lose the prefix cache. Options:
  - (a) Apply the change only at a user-message boundary (a new assistant turn) and accept one cache miss.
  - (b) Anthropic tool search with `defer_loading`, or beta `tool_addition` blocks: they keep the cache but are provider-specific.
  - (c) A stable `connector_call(tool, args)` meta-tool plus an index: stable, but it hides schemas from the model.
  - Recommend (a) now and (b) later, once a project has more than ~30 tools.
- Tool-count guidance: Gemini recommends 10–20 active tools ([function calling](https://ai.google.dev/gemini-api/docs/generate-content/function-calling)); Anthropic's tool search accuracy degrades past 30–50.

---

## 7. Go-specific design

### 7.1 Package layout (suggestion)

```
internal/
  msg/          neutral message format: Message, Part (Text, Thinking, ToolUse, ToolResult), ADR 0002
  llm/          Provider interface, Request/Response, StopReason, *Error{Kind, RetryAfter, RequestID}, retry policy
  llm/anthropic llm/openai llm/gemini   adapters: msg <-> SDK types; the only place with provider quirks
  llm/fake      scripted Provider for tests
  tool/         Tool, ToolDef, Registry, Result; tool/builtin/{websearch,webfetch,shell,files,memory,delegate}
  mcp/          Connector client -> tool.Tool adapter
  prompt/       Project(state, defs) -> llm.Request   (pure; context.md)
  loop/         State, Fold, Decide (pure), Step, Hook, exec
  approver/     rules -> Jev -> small LLM; implements loop.Hook
  eventlog/     Store.Append/Load, upcasters
  worker/       claim, heartbeat, drive (event-log §5.3)
  sandbox/      Sandbox interface, docker impl
```

Dependency direction: `worker → loop → {prompt, llm, tool, eventlog}`; adapters depend on `msg`, never the reverse.

### 7.2 Interfaces

```go
// llm
type Provider interface {
    // Stream sends deltas to onDelta (UI only) and returns the final accumulated response.
    Stream(ctx context.Context, req Request, onDelta func(Delta)) (*Response, error)
    CountTokens(ctx context.Context, req Request) (int, error) // or estimate
    Models() []ModelInfo                                       // window, max output, pricing
}
type Response struct { Message msg.Message; Stop StopReason; Usage Usage; RequestID string }
type StopReason string // end_turn, tool_use, max_tokens, refusal, context_exceeded, other

// tool
type Tool interface {
    Def() ToolDef
    Call(ctx context.Context, in CallInput) (Result, error) // error = bug/infra; exec converts it to an is_error Result
}
type ToolDef struct {
    Name, Description string; Schema json.RawMessage
    ReadOnly, Destructive, ParallelSafe, Untrusted bool // seeded from MCP hints; Untrusted feeds taint
    Timeout time.Duration; Source string                 // "builtin" | "connector:<id>"
}
type CallInput struct { CallID, IdempotencyKey string; Args json.RawMessage; Sandbox sandbox.Handle }
type Registry interface {
    Defs(st SessionView) []ToolDef // deterministic order; frozen per assistant turn (Q4)
    Get(name string) (Tool, bool)
}
```

- The callback-style `Stream` is simplest to learn and to fake. The SDKs expose iterators (`ssestream.Stream` with `Next/Current/Err` in [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) and [openai-go](https://github.com/openai/openai-go); `iter.Seq2` in [go-genai](https://github.com/googleapis/go-genai)). The adapter hides them.
- In anthropic-sdk-go, accumulate with `message.Accumulate(event)`.

### 7.3 Cancellation and goroutines

- `drive` owns `ctx, cancel := context.WithCancelCause(parent)`. The cause says *why* it was cancelled (`ErrUserInterrupt`, `ErrLeaseLost`, `ErrShutdown`), read back with `context.Cause(ctx)` ([context](https://pkg.go.dev/context)). `exec` picks `turn.interrupted{user_interrupt}` vs "just return".
- **Gotcha**: after cancel, the ctx is dead, so the "append interrupted" write needs `context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)`. `WithoutCancel` "is not canceled when parent is canceled".
- **Provider**: pass ctx into the SDK. Cancelling aborts the HTTP body read, and SDK retry sleeps return `ctx.Err()`. A per-attempt timeout (`option.WithRequestTimeout`) is separate from the ctx deadline ([openai-go README](https://github.com/openai/openai-go/blob/main/README.md)). Add our own stream-idle watchdog (Codex uses 300 s).
- **Tools**: `tctx, cancel := context.WithTimeout(ctx, def.Timeout)` per call.
- **Sandbox exec**:
  - Locally, `exec.CommandContext` kills the process on ctx done, and `Cmd.WaitDelay` bounds hung pipes ([os/exec](https://pkg.go.dev/os/exec)).
  - Docker, however, has **no API to kill an exec process** ([moby#35703](https://github.com/moby/moby/issues/35703)). Closing the attach stream does not stop it. So `Sandbox.Exec` must start commands in their own process group and record the PID, then on `ctx.Done()` run a second exec: `kill -TERM -<pgid>`, wait for a grace period, then `-KILL`.
  - Write this into the Sandbox interface contract and cover it with a test.
- **Remote MCP**: cancel the ctx and send `notifications/cancelled`. Server behaviour is **unverified** (event-log §3.8).
- **Goroutine rules**:
  - Per session: one `drive` goroutine plus one heartbeat. Per tool batch: N goroutines bounded by a semaphore. The Provider stream is read inside `drive` (callback), not in a separate goroutine.
  - Tool goroutines send `result` values on a channel. **Only `drive` appends**, so there is a single in-process writer per session.
  - `drive` waits for all its children (WaitGroup) before returning, so no goroutine outlives the lease.
  - Test with `go test -race` and [goleak](https://github.com/uber-go/goleak).

### 7.4 Testing with a fake Provider

```go
p := fake.New(
    fake.ToolUse("web_search", `{"q":"trains"}`),       // turn 1
    fake.Err(&llm.Error{Kind: llm.Overloaded}),        // turn 2, attempt 1
    fake.Text("Here are options…"),                    // turn 2, attempt 2
    fake.BlockUntilCancel(),                           // for Interrupt tests
)
// run the worker until Park, then assert on:
golden.Assert(t, eventTypes(store.Load(sid)), "testdata/search_then_answer.golden")
require.Equal(t, 3, len(p.Requests()))  // and inspect prompts: cache-stable prefix, tool_result order
```

- A fake Tool gets the same treatment: `fake.Tool("shell", sleep 10s | fail | ok)`.
- Tables of `(log tail) → Decide → step` cover recovery with no I/O.
- Adapter tests: golden JSON of `msg → provider request` per provider, especially thinking replay, foreign-provider history, and parallel result ordering.

---

## 8. Options and recommendation

| | A. Classic in-process loop | B. Step machine + typed hooks (recommended) | C. Wrapping middleware chain |
|---|---|---|---|
| Shape | `for { resp := model(); if no tools break; run tools }`, logging as a side effect | `Decide` picks one step; `exec` runs it with `Hook`s; every outcome is appended | `handler = mw1(mw2(core))` around model and tool calls (LangChain style) |
| Fits event-log fold/Decide | poorly: loop state lives in locals, crash = ad-hoc recovery | exactly | partly: middleware can hide work between appends |
| Approvals parking for days | awkward (must exit the loop) | natural (`Park`) | awkward |
| Retry / fallback / tracing | inline | Provider decorators | elegant |
| Learning value | low | high | medium |

**Recommend B.** Use C-style decorators only *below* `Provider` and `Tool` (retry, OTel, metering counters), never around the step.

One loop iteration (Go-ish; `Append` = event-log `Store.Append` with the fence):

```go
func (x *Exec) run(ctx context.Context, st *State, step Step) error {
    switch step.Kind {
    case StartTurn: // Decide already checked Budget + Compaction thresholds
        defs := x.reg.Defs(st.View())
        req := prompt.Project(st, defs)        // pure; BP3 on last block
        v, err := x.hooks.BeforeModel(ctx, st, &req); if err != nil || v.Park != nil { return x.park(ctx, v) }
        turnID := newID()
        x.append(ctx, v.Events, ev.TurnStarted{turnID, req.Model, st.LastInputSeq, hash(defs)})
        resp, err := x.retry.Do(ctx, func(c context.Context) (*llm.Response, error) {
            return x.prov.Stream(c, req, func(d llm.Delta) { x.bus.Publish(st.ID, turnID, d) })
        })
        if err != nil { return x.providerFailure(ctx, turnID, err) } // interrupt | timer.set | session.error
        v, _ = x.hooks.AfterModel(ctx, st, resp)
        return x.append(ctx, v.Events,
            ev.LLMResponse{turnID, resp.Message, resp.Stop, resp.Usage},
            ev.UsageRecorded{Kind: "llm", ...}) // one tx; next Decide sees tool_use blocks

    case RequestTools: // all tool_use blocks of the last llm.response, in order
        var evs []NewEvent; asks := 0
        for _, c := range step.Calls {
            evs = append(evs, ev.ToolCallRequested{c.ID, c.Name, c.Args})
            tv, _ := x.hooks.BeforeTool(ctx, st, c)   // Approver runs here
            evs = append(evs, tv.Events...)
            switch tv.Decision {
            case Deny: evs = append(evs, ev.ToolCallCompleted{c.ID, denied(tv.Reason), true})
            case Ask:  evs = append(evs, ev.ApprovalRequested{newID(), c.ID, tv.By}); asks++
            }
        }
        return x.appendMaybePark(ctx, evs, asks > 0) // park awaiting_approval if any ask

    case StartTools: // approved, not-started calls; Decide splits into parallel-safe batch or single
        x.append(ctx, nil, startedEvents(step.Calls)...) // fenced intent; fail => call nothing
        results := make(chan done)
        var wg sync.WaitGroup
        for _, c := range step.Calls {
            wg.Add(1)
            go func() { defer wg.Done(); results <- x.callTool(ctx, st, c) }() // timeout, is_error on any failure
        }
        go func() { wg.Wait(); close(results) }()
        for r := range results { // only drive appends; completion order
            if ctx.Err() != nil { break }
            v, _ := x.hooks.AfterTool(ctx, st, r.call, &r.res) // blob_ref for large output
            x.append(ctx, v.Events, ev.ToolCallCompleted{r.call.ID, r.res.Content, r.res.IsError})
        }
        wg.Wait()
        return ctx.Err() // drive: Cause == ErrUserInterrupt => MarkInterrupted path (§4)
    }
}
```

After `StartTools`, the next `Decide` sees all results. If `user.message.seq > input_through_seq` it records the Steering, and either way the next step is `StartTurn`. The projection puts steering text after the tool results in the same user message.

---

## 9. Open questions (with recommended answers)

1. **Run a turn's tool calls at the same time?** Rec: only tools marked `ParallelSafe` (read-only built-ins such as web search/fetch, `memory view`, and MCP tools with `readOnlyHint`), at most 4 at once. Everything else, especially shell and file edits in the Sandbox, runs one at a time in the order the model asked. This is what Claude Code and Codex do.
2. **If 3 tools are requested and one needs approval, do the other two run first?** Rec: no. Run the Approver on all of them, and if any is "ask", pause before starting any. When the user answers, run the batch. It is simpler, and all results still come back together as the providers require.
3. **Where do we record that the Approver *allowed* a call?** The event catalog only has `approval.requested` for "ask". Rec: add `approved_by` and the approver trace (rule id / Jev confidence) to the `tool.call.started` payload rather than a new event type. This needs a nod in event-log.md.
4. **What if the tool list changes mid-session (a Connector added, MCP `list_changed`)?** Rec: freeze the list for the current assistant turn and apply the change at the next user message. Record a hash of the tool list on `turn.started` and keep the full list as a blob, so replay rebuilds the same prompt. One cache miss per change is acceptable.
5. **Thinking blocks vs our Compaction (new Anthropic prefix binding).** Tier 1 clearing and Tier 2 keep-last-K both edit the earlier prompt. On newer Claude models (Fable 5.1 / Opus 5.5 per the docs), that makes the later thinking blocks invalid, and the call fails with a 400 on accounts created after 2026-08-31. Rec: after any clear or summary, the projection drops every thinking block that sits after the first edited spot. Deleting blocks from the end is explicitly allowed. The fix is deterministic and works for every Provider. context.md currently says "Thinking-block clearing: not used yet", so that line needs updating. Optionally also send the `drop_block` beta setting as a safety net.
6. **Thinking from a different Provider after `/model`?** Rec: drop it (keep only its text and tool calls). On Gemini, add the documented dummy signature to old function calls. Don't paste another model's reasoning in as text.
7. **Who retries failed LLM calls, the SDK or us?** Rec: us. Set the SDK's retries to 0 and implement the §3.6 table: up to 4 quick retries (honoring `retry-after`), a sleep via `timer.set` for long waits so no Worker is held, and stop with a clear message on key or billing errors. That way every attempt is visible and metered.
8. **Switch to a backup model automatically when one is overloaded or refuses?** Rec: not in the base version. With BYOK, a silent switch spends the user's money on a model they didn't choose. Show the error and let them use `/model`.
9. **The reply was cut off (`max_tokens`).** Rec: throw away any half-written tool call and retry once with the model's maximum output. If it is still cut off, keep the text and wait for the user.
10. **After an Interrupt, auto-send messages the user queued?** Rec: no. Interrupt means "stop"; go to `awaiting_user` and let queued Steering become the next normal message. This differs from Claude Code's Esc (**unverified** whether users expect Claude's behaviour here).
11. **Timeout for a tool call?** Rec: 120 s default, shell up to the Sandbox wall-clock cap, overridable per `ToolDef`. On timeout, kill the process group (Docker can't kill an exec by itself, §7.3) and return an error the model can read. Never retry automatically.
12. **Loop detection (the same failing call again and again)?** Rec: later. The Budget turn limit already stops runaways. If it becomes a problem, copy Gemini CLI: 5 identical calls in a row → inject one warning, then pause.
13. **Can hooks change a tool's arguments (like Claude's `updatedInput`)?** Rec: no, not in the base version. Allow, ask, deny and extra events are enough, and the log stays easy to read.
14. **Callback or iterator for streaming (`Stream(ctx, req, onDelta)` vs `iter.Seq2`)?** Rec: callback. It is simpler to write, to fake in tests, and to reason about when the ctx is cancelled.

---

## 10. Sources

- Anthropic (docs): https://code.claude.com/docs/en/agent-sdk/agent-loop ; https://code.claude.com/docs/en/agent-sdk/permissions ; https://code.claude.com/docs/en/agent-sdk/hooks ; https://code.claude.com/docs/en/agent-sdk/streaming-vs-single-mode ; https://code.claude.com/docs/en/how-claude-code-works ; https://code.claude.com/docs/en/interactive-mode ; https://code.claude.com/docs/en/hooks ; https://platform.claude.com/docs/en/build-with-claude/handling-stop-reasons ; https://platform.claude.com/docs/en/agents-and-tools/tool-use/handle-tool-calls ; https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use ; https://platform.claude.com/docs/en/agents-and-tools/tool-use/fine-grained-tool-streaming ; https://platform.claude.com/docs/en/build-with-claude/streaming ; https://platform.claude.com/docs/en/build-with-claude/thinking ; https://platform.claude.com/docs/en/build-with-claude/preserved-thinking ; https://platform.claude.com/docs/en/api/errors ; https://platform.claude.com/docs/en/build-with-claude/prompt-caching ; https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool
- Anthropic (source): https://github.com/anthropics/anthropic-sdk-go ; https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/_internal/sessions.py
- Anthropic engineering: https://www.anthropic.com/engineering/building-effective-agents ; https://www.anthropic.com/engineering/effective-context-engineering-for-ai-agents ; https://www.anthropic.com/engineering/writing-tools-for-agents ; https://www.anthropic.com/engineering/multi-agent-research-system
- OpenAI Agents SDK: https://openai.github.io/openai-agents-python/running_agents/ ; https://openai.github.io/openai-agents-python/handoffs/ ; https://openai.github.io/openai-agents-python/guardrails/ ; https://openai.github.io/openai-agents-python/agents/ ; https://openai.github.io/openai-agents-python/tools/ ; https://openai.github.io/openai-agents-python/human_in_the_loop/ ; https://openai.github.io/openai-agents-python/ref/lifecycle/ ; https://github.com/openai/openai-agents-python/tree/main/src/agents/run_internal
- OpenAI API: https://developers.openai.com/api/docs/guides/function-calling ; https://developers.openai.com/api/docs/guides/reasoning ; https://developers.openai.com/api/docs/guides/rate-limits ; https://developers.openai.com/api/docs/guides/prompt-caching ; https://github.com/openai/openai-go
- Codex (`codex-rs/`): https://github.com/openai/codex/blob/main/codex-rs/core/src/session/turn.rs ; https://github.com/openai/codex/blob/main/codex-rs/core/src/session/turn_input.rs ; https://github.com/openai/codex/blob/main/codex-rs/core/src/tasks/mod.rs ; https://github.com/openai/codex/blob/main/codex-rs/core/src/context/turn_aborted.rs ; https://github.com/openai/codex/blob/main/codex-rs/core/src/tools/parallel.rs ; https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs ; https://github.com/openai/codex/blob/main/codex-rs/core/src/exec.rs ; https://github.com/openai/codex/blob/main/codex-rs/app-server-protocol/src/protocol/v2/hook.rs
- Gemini CLI (`packages/`): https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/client.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/geminiChat.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/scheduler/scheduler.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/scheduler/state-manager.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/utils/retry.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/fallback/handler.ts ; https://github.com/google-gemini/gemini-cli/blob/main/packages/cli/src/ui/hooks/useGeminiStream.ts ; https://github.com/google-gemini/gemini-cli/blob/main/docs/hooks/reference.md
- Gemini API: https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures ; https://ai.google.dev/gemini-api/docs/generate-content/function-calling ; https://ai.google.dev/api/generate-content ; https://ai.google.dev/gemini-api/docs/generate-content/caching ; https://github.com/googleapis/go-genai
- LangChain / LangGraph: https://docs.langchain.com/oss/python/langgraph/graph-api ; https://docs.langchain.com/oss/python/langgraph/interrupts ; https://docs.langchain.com/oss/python/langchain/middleware/custom ; https://docs.langchain.com/oss/python/langchain/middleware/built-in ; https://raw.githubusercontent.com/langchain-ai/langchain/master/libs/langchain_v1/langchain/agents/factory.py ; https://raw.githubusercontent.com/langchain-ai/langgraph/main/libs/prebuilt/langgraph/prebuilt/tool_node.py
- Letta (tag 0.16.8): https://github.com/letta-ai/letta/blob/0.16.8/letta/agents/letta_agent_v3.py ; https://github.com/letta-ai/letta/blob/0.16.8/letta/schemas/letta_stop_reason.py ; https://github.com/letta-ai/letta/blob/0.16.8/letta/schemas/tool_rule.py
- Pydantic AI: https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/_agent_graph.py ; https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/usage.py ; https://pydantic.dev/docs/ai/tools-toolsets/tools-advanced/ ; https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/models/fallback.py
- Vercel AI SDK: https://github.com/vercel/ai/blob/main/packages/ai/src/generate-text/generate-text.ts ; https://github.com/vercel/ai/blob/main/content/docs/03-agents/04-loop-control.mdx ; https://github.com/vercel/ai/blob/main/content/docs/03-ai-sdk-core/15-tools-and-tool-calling.mdx ; https://github.com/vercel/ai/blob/main/packages/anthropic/src/convert-to-anthropic-prompt.ts
- Go: https://pkg.go.dev/golang.org/x/sync/errgroup ; https://pkg.go.dev/context ; https://pkg.go.dev/os/exec ; https://github.com/uber-go/goleak ; https://github.com/moby/moby/issues/35703
