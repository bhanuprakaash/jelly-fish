# Provider Gateway (Neutral Message Format): Research

Status: research, 2026-09-26. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Facts cite primary sources inline (provider docs, official Go SDK source on `main` as of 2026-09-26, first-party blogs). **Unverified** marks anything not confirmed from a primary source. "⚠ cross-spec" flags a conflict with an existing ADR or design doc; it is flagged, not silently resolved.

> Background only. Fixed and not redesigned here: ADR [0002](../adr/0002-neutral-message-format.md) (own neutral format, Providers are adapters, no multi-provider library), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (BYOK for LLMs), [product.md](../product.md) (Provider Keys envelope-encrypted, decrypted only in the Worker, never sent to the frontend), [agent-loop.md](../design/agent-loop.md) (`Stream(ctx, req, onDelta func(Delta)) (*Response, error)`, `StopReason` enum, SDK retries = 0 and our retry table, decorators only below `Provider`, `ThinkingPart{text, provider, model, opaque}`, Gemini dummy signature, OpenAI `truncation:"disabled"` + `prompt_cache_key=session_id`, 3rd moving cache breakpoint, fake-Provider tests), [context.md](../design/context.md) (prompt layout, breakpoints, Compaction, thinking drop), [event-log.md](../design/event-log.md) (payloads are neutral format, `usage.recorded`, `schema_version` + upcasters, `blob_ref`). This doc covers what sits inside the `Provider` box: the `msg` types, the three adapters, and the tables they need.

---

## 1. Where the gateway sits

- Call site: `exec(StartTurn)` runs `prompt.Project(state, defs) → llm.Request` (pure), then `Provider.Stream(ctx, req, onDelta)`, then appends `llm.response` + `usage.recorded` in one tx ([agent-loop.md §5.2](../design/agent-loop.md)). Adapters never touch the Event Log.
- Layers, from [agent-loop research §7.1](agent-loop.md):
  ```
  internal/msg/            Message, Part (neutral, ADR 0002) + its upcasters
  internal/llm/            Provider, Request/Response, Delta, StopReason, Usage, ModelInfo, *Error{Kind, RetryAfter, RequestID}
  internal/llm/anthropic   internal/llm/openai   internal/llm/gemini   msg <-> official SDK types; the only place with Provider quirks
  internal/llm/catalog     static model/price table (§7)
  internal/llm/fake        scripted Provider (agent-loop.md §7.4)
  ```
- Decorators (`func(Provider) Provider`: retry, OTel, metering) wrap an adapter; they are not part of the translation.
- Each adapter has three jobs: **request** (tools, system, messages, tool_choice, thinking config, cache breakpoints), **stream** (SSE/chunks → `Delta`s, accumulate), **response** (→ `msg.Message` + `StopReason` + `Usage` + `RequestID`), plus error classification into agent-loop.md §5.3's classes.
- The key is decrypted in the Worker and handed to the SDK client per call (`option.WithAPIKey`). No other process sees it (product.md). This alone rules out any proxy that needs the key (§11.3).

---

## 2. Prior art survey

- **Vercel AI SDK** (`@ai-sdk/provider`, now `LanguageModelV3`): `LanguageModelV3Content = Text | Reasoning | File | ToolApprovalRequest | Source | ToolCall | ToolResult` ([v3 content](https://github.com/vercel/ai/blob/main/packages/provider/src/language-model/v3/language-model-v3-content.ts)). Reasoning is `{type:'reasoning', text, providerMetadata?}` ([v3 reasoning](https://github.com/vercel/ai/blob/main/packages/provider/src/language-model/v3/language-model-v3-reasoning.ts)): the Provider's opaque bytes ride in `providerMetadata`, the same idea as our `ThinkingPart.opaque`. The Anthropic converter drops reasoning without an Anthropic signature ([convert-to-anthropic-prompt.ts](https://github.com/vercel/ai/blob/main/packages/anthropic/src/convert-to-anthropic-prompt.ts)). Versioning is on the *interface name* (V1→V2→V3), not on each payload.
- **LiteLLM**: its lingua franca is the OpenAI Chat Completions shape (`role`, `content`, `tool_calls`), and every other Provider is translated onto and off it ([BerriAI/litellm](https://github.com/BerriAI/litellm)). It adopts one Provider's format rather than owning a neutral one. **Unverified** in detail (not re-read at source level).
- **Pydantic AI**: `ModelMessage = ModelRequest | ModelResponse`. Request parts include `SystemPromptPart`, `UserPromptPart`, `ToolReturnPart`, `RetryPromptPart`; response parts include `TextPart`, `ToolCallPart{tool_name, args, tool_call_id}`, `ThinkingPart{content, signature, provider_name, provider_details}`, and a separate part for built-in (server) tool calls. Multimodal input is `BinaryContent`/`ImageUrl`/`DocumentUrl`/…, plus `UploadedFile` for a Provider file id ([messages.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/messages.py), [messages reference](https://pydantic.dev/docs/ai/api/pydantic-ai/messages/)). It sends a native thinking block only when `provider_name` matches ([anthropic.py](https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/models/anthropic.py)).
- **LangChain v1** "standard content blocks": `TextContentBlock`, `ReasoningContentBlock`, data blocks (`Image`/`Audio`/`Video`/`File`/`PlainText`), `ToolCall`/`ToolCallChunk`/`InvalidToolCall`, `ServerToolCall`/`ServerToolCallChunk`/`ServerToolResult`, and a `NonStandardContentBlock` escape hatch for Provider-specific data. It is exposed as a lazily parsed `content_blocks` view over the Provider-native `content` ([messages docs](https://docs.langchain.com/oss/python/langchain/messages), [content reference](https://reference.langchain.com/python/langchain-core/messages/content)). This is the closest analogue to ADR 0002, and the escape-hatch block is worth copying (§3.1).
- **OpenAI Agents SDK**: no neutral format. It works in Responses API input/output items and wraps them as `RunItem`s (`MessageOutputItem`, `ToolCallItem`, `ToolCallOutputItem`, `ReasoningItem`, `HandoffCallItem`) ([running agents](https://openai.github.io/openai-agents-python/running_agents/)). It is OpenAI-only, so it doesn't need one.
- **Gemini CLI**: stores history as native Gemini `Content`/`Part` and patches thought signatures in place (`ensureActiveLoopHasThoughtSignatures`) rather than converting ([geminiChat.ts](https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/geminiChat.ts)). Gemini-only.
- **Codex CLI**: `pub enum ResponseItem` in `codex-rs/protocol/src/models.rs`, with variants `Message`, `AgentMessage`, `Reasoning`, `LocalShellCall`, `FunctionCall`, `FunctionCallOutput`, `CustomToolCall(Output)`, `ToolSearchCall/Output`, `WebSearchCall`, `ImageGenerationCall`, `Compaction`, `ContextCompaction`, …, `Other` ([models.rs](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/models.rs)). It mirrors Responses items, with an `Other` catch-all for forward compatibility.

### Comparison

| Project | Own neutral format? | Message shape | Thinking/opaque bytes | Server-tool parts | Escape hatch | Payload version |
|---|---|---|---|---|---|---|
| Vercel AI SDK | yes | role + content-part union | `providerMetadata` | via ToolCall/ToolResult (`providerExecuted`) (**Unverified** flag name) | `providerMetadata` | interface name (V3) |
| LiteLLM | no, adopts OpenAI Chat shape | `role/content/tool_calls` | provider-specific fields | no | provider fields | package version |
| Pydantic AI | yes | request/response split | `signature`, `provider_details` | yes | `provider_details` | package version |
| LangChain v1 | yes | `content_blocks` view | `ReasoningContentBlock` | yes | `NonStandardContentBlock` | package version |
| OpenAI Agents SDK | no (Responses items) | items + `RunItem` | native | native | — | — |
| Gemini CLI | no (Gemini native) | `Content/Part` | native `thoughtSignature` | native | — | — |
| Codex CLI | no (Responses-like enum) | `ResponseItem` | native `Reasoning` | native | `Other` | — |
| **jelly-fish** | yes (ADR 0002) | `Message{Role, Parts}` | `ThinkingPart.opaque` (fixed) | reserved (§3.1) | `NativePart` (proposed) | see §3.3 |

Lessons: (1) every cross-Provider framework lands on "typed part + opaque Provider bag", which validates `ThinkingPart.opaque`; (2) two of them keep an escape hatch so a new Provider block type doesn't force a schema change; (3) nobody versions the stored payload itself. They version packages. jelly-fish has to do better, because its payloads live forever in the Event Log.

---

## 3. Neutral format

### 3.1 Part types and per-Provider mapping

| Neutral part | Anthropic Messages | OpenAI Responses | Gemini `generateContent` |
|---|---|---|---|
| `TextPart` | `{"type":"text","text"}`; `text` has `minLength: 1` ([Messages API](https://platform.claude.com/docs/en/api/messages)) | `input_text` / `output_text` content inside a `message` item | `Part{text}` |
| `ImagePart` (blob) | `{"type":"image","source":{"type":"base64"\|"url"\|"file"}}` | `input_image` (data URL, URL, or `file_id`) | `Part{inlineData:{mimeType,data}}` or `Part{fileData:{mimeType,fileUri}}` ([go-genai types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
| `FilePart` (PDF/text, blob) | `{"type":"document","source":{base64\|text\|url\|file\|content},"citations","title","context"}` | `input_file` (base64 or `file_id`) | `inlineData`/`fileData` with `application/pdf` |
| `ToolUsePart{ID, Name, Args}` | `{"type":"tool_use","id","name","input"}` | top-level `function_call` item `{id:"fc_…", call_id:"call_…", name, arguments}`; results match on `call_id` ([function calling](https://developers.openai.com/api/docs/guides/function-calling)) | `Part{functionCall:{id, name, args}}`; `id` is optional: "If populated, the client to execute the `function_call` and return the response with the matching `id`" ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
| `ToolResultPart{CallID, Parts, IsError}` | `{"type":"tool_result","tool_use_id","content": string \| [text,image,…],"is_error"}` | top-level `function_call_output{call_id, output}`; `output` is a string, or "an array of image or file objects" ([function calling](https://developers.openai.com/api/docs/guides/function-calling)); the SDK comment reads "Text, image, or file output" ([openai-go response.go](https://github.com/openai/openai-go/blob/main/responses/response.go)) | `Part{functionResponse:{id, name, response: object, parts:[inlineData…]}}`; `response` uses an `"output"`/`"error"` key convention; `parts` carries inline media (URI `fileData` "not supported in Gemini API") ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
| `ThinkingPart{Text, Provider, Model, Opaque}` (fixed) | `thinking{thinking, signature}` and `redacted_thinking{data}`, both replayed "exactly as received" ([errors](https://platform.claude.com/docs/en/api/errors)) | `reasoning` item; with `store:false`, `encrypted_content` is included "by default"; the legacy `include` value is "accept[ed]… but [not] require[d]" ([reasoning](https://developers.openai.com/api/docs/guides/reasoning)) | `Part{thought:true, text}` (summaries) plus a `thoughtSignature []byte` on a Part ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go), [thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)) |
| `Citation` (on `TextPart`) | `citations[]` on text blocks, `citations_delta` in streams ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)) | `annotations` on `output_text`, `response.output_text.annotation.added` event ([response.go](https://github.com/openai/openai-go/blob/main/responses/response.go)) | `candidate.citationMetadata`, `groundingMetadata` (per candidate, not per part) ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
| Server-tool blocks (reserved) | `server_tool_use` + `web_search_tool_result`, `web_fetch_tool_result`, `code_execution_tool_result`, `tool_search_tool_result`, … ([Messages API](https://platform.claude.com/docs/en/api/messages)) | `web_search_call`, `file_search_call`, `code_interpreter_call`, `mcp_call`, … items | `executableCode`, `codeExecutionResult`, `toolCall`/`toolResponse` parts |

Notes:
- Our built-in tools (web search via `SearchProvider`, web fetch, shell/files in the Sandbox) are **client-side** Tools (product.md). Provider server tools are not used in the base version. Reserve the *shape* (as a `NativePart`, below) instead of modelling each one.
- **Shape mismatch 1**: Anthropic and Gemini nest tool calls/results as parts inside a message, while OpenAI Responses uses top-level items next to `message` items. The OpenAI adapter flattens `Message.Parts` into items on the way out and folds items back into one `msg.Message` on the way in.
- **Shape mismatch 2**: all three accept *multiple* parts in a tool result (text + image). ⚠ cross-spec: agent-loop.md §4.2 has `tool.Result{Content msg.Part}`, a single part. It needs to be `[]msg.Part` (or a `ToolResultPart` holding `Parts`) to carry "text + screenshot" results without flattening. Needs a nod in agent-loop.md.
- **Shape mismatch 3**: Gemini puts citations on the candidate and signatures on arbitrary Parts. The adapter maps both to part-level fields.

Proposed Go shape (a tagged struct, so it is jsonb-friendly and upcastable; illustrative only):

```go
package msg

type Role string // "user" | "assistant". System content is Request.System, not a message (context.md §5.2)

type Message struct {
    Role  Role   `json:"role"`
    Parts []Part `json:"parts"`
}

type Part struct {
    Kind       Kind        `json:"k"`                // text|image|file|tool_use|tool_result|thinking|native
    Text       string      `json:"text,omitempty"`
    Citations  []Citation  `json:"cit,omitempty"`
    Blob       *BlobRef    `json:"blob,omitempty"`   // image/file: event-log blob_ref {key,size,sha256,mime}
    ToolUse    *ToolUse    `json:"tu,omitempty"`     // {ID, Name, Args json.RawMessage, Opaque []byte}
    ToolResult *ToolResult `json:"tr,omitempty"`     // {CallID, Parts []Part, IsError}
    Thinking   *Thinking   `json:"th,omitempty"`     // {Text, Provider, Model, Opaque []byte}  (fixed)
    Native     *Native     `json:"nat,omitempty"`    // {Provider, Type, Raw json.RawMessage}: replayed only to the same Provider
}
```

- `ToolUse.ID` is **ours**: always set, even when the Provider gave none (older Gemini). The adapter keeps the Provider's own id (OpenAI `fc_…` item id, Gemini `functionCall.id`) in `ToolUse.Opaque` for same-Provider replay.
- A Gemini `thoughtSignature` attached to a `functionCall` goes in that ToolUse's `Opaque`. A dropped signature is replaced with the dummy on replay (agent-loop.md Decision 6).
- `Native` is the LangChain-style escape hatch. It is replayed to the same Provider and dropped for a foreign one, like thinking. The core never interprets it.

### 3.2 Roles and system placement

| | Anthropic | OpenAI Responses | Gemini |
|---|---|---|---|
| Roles in history | `user`, `assistant` only | `user`, `assistant`, `system`, `developer` input items; tool calls/results are items, not roles | `user`, `model` |
| System prompt | top-level `system` (string or array of text blocks with `cache_control`) ([Messages API](https://platform.claude.com/docs/en/api/messages)) | `instructions` param, or a leading `developer` item ([text generation](https://developers.openai.com/api/docs/guides/text-generation)) | top-level `systemInstruction` (a `Content`) |
| Consecutive same role | "Consecutive `user` or `assistant` turns in your request will be combined into a single turn" ([Messages API](https://platform.claude.com/docs/en/api/messages)) | accepted (item list) (**Unverified**: no explicit rule found) | **Unverified** |
| Empty content | `text` `minLength: 1`; empty thinking blocks must still be passed back ([errors](https://platform.claude.com/docs/en/api/errors)) | **Unverified** | **Unverified** |
| Must end with user | yes on 4.6+ models: prefill gives a 400 "The conversation must end with a user message" ([errors](https://platform.claude.com/docs/en/api/errors)) | no | **Unverified** |

- Neutral `Role` is `user | assistant`, and system lives in `Request.System []TextPart` (so Anthropic can put `cache_control` on system blocks). The OpenAI adapter sends it as a `developer` item rather than `instructions` so it can live inside the cached prefix. **Unverified** whether caching treats `instructions` differently.
- The projection should never emit adjacent same-role messages or empty text. It merges them itself (agent-loop.md already puts tool results + Steering text in one user message). That is the strictest rule, and it is safe for all three.

### 3.3 Versioning inside stored payloads

- ⚠ cross-spec: event-log.md §5.4 says "The neutral message format carries its own version inside the payload (ADR 0002)", but ADR 0002's text does not say this. The two docs need to agree.
- Option (i): **one `msg_v` integer inside every payload that embeds `msg`** (`llm.response`, `user.message`, `tool.call.completed`), upcast by `msg.Upcast(v, raw)`. Adding a `Part.Kind` is additive (no bump). A shape change bumps `msg_v`. This matches the event-log.md sentence as written.
- Option (ii): rely only on each event type's `schema_version`. It is simpler, but a `msg` change then bumps every embedding event type at once.
- No prior art stores a payload version (§2), so this is our call. Rec: (i), because it matches the already-accepted event-log text and keeps `msg` upcasters in one package. Unknown `Kind` on read → a Worker that is too old releases the Lease (event-log.md §5.19 rule), the same as an unknown `schema_version`. See Q1.

---

## 4. Per-provider pain points

### 4.1 Which OpenAI API: Responses (decided here)

- OpenAI: "While Chat Completions remains supported, Responses is recommended for all new projects" ([migrate to Responses](https://developers.openai.com/api/docs/guides/migrate-to-responses)).
- Reasoning passback exists only in Responses: stateless `encrypted_content`, and "pass back all reasoning items, function call items, and function call output items, since the last `user` message" ([reasoning](https://developers.openai.com/api/docs/guides/reasoning)).
- The same guide says: "Use the Responses API for function calling. Chat Completions does not support function calling with GPT-6 Astra" ([reasoning](https://developers.openai.com/api/docs/guides/reasoning)). For an agent runtime, that settles it.
- agent-loop.md already assumes Responses (`truncation:"disabled"`, reasoning items with tool outputs). openai-go ships both under separate packages (`responses`, `chat`) ([openai-go](https://github.com/openai/openai-go)).
- **Use `store:false` and resend the full projected prompt every turn.** Don't use `previous_response_id`: the Event Log is the source of truth, and context.md Decision 1 already rejects opaque Provider-side state. Same reasoning, not a new decision.
- Rec: **Responses only.** Don't build a Chat Completions adapter. OpenAI-compatible third parties (GLM, Ollama, roadmap) may need a Chat Completions adapter later. That is a separate adapter, not a mode of this one.

### 4.2 Tool call ids and parallel calls

| | Anthropic | OpenAI Responses | Gemini |
|---|---|---|---|
| Call id | `tool_use.id` ↔ `tool_result.tool_use_id` | `function_call.call_id` ↔ `function_call_output.call_id` (item also has `id: fc_…`) | `functionCall.id` ↔ `functionResponse.id`, optional ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)); when absent, match by name + order |
| Disable parallel | `tool_choice.disable_parallel_tool_use` ([Messages API](https://platform.claude.com/docs/en/api/messages)) | `parallel_tool_calls:false` → "exactly zero or one tool is called" ([function calling](https://developers.openai.com/api/docs/guides/function-calling)) | none found (**Unverified**) |
| Result ordering | all results in the next user message, results before text ([parallel tool use](https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use)) | by `call_id` | same order as calls; FC/FR interleaving → 400 ([thought signatures](https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures)) |

- The fork drafts disagreed on Gemini ids. The go-genai source is authoritative: the field exists and is optional. Which Gemini models populate it is **Unverified**. The adapter always fills `functionResponse.id` when the call had one, and otherwise relies on the order that agent-loop.md's "projection re-sorts results into tool_use order" rule already guarantees.
- Anthropic ids must match `^[a-zA-Z0-9_-]+$`. **Unverified** (from a fork's read of the Messages API page). Our own ids (e.g. ULIDs) comply. When replaying foreign-Provider history, the adapter should rewrite call ids into a safe charset, consistently for call and result.

### 4.3 Tool result shapes

- Anthropic: string or blocks (text, image, …). OpenAI: string or array of image/file objects. Gemini: JSON object (`response`) plus inline-media `parts`.
- Rule: `ToolResultPart.Parts` = text and/or image/file parts. A structured-JSON result is a `TextPart` holding JSON. For Gemini the adapter wraps it as `{"output": <parsed or string>}` and, when `IsError`, as `{"error": …}`, per the go-genai key convention.
- Anthropic has native `is_error`. OpenAI and Gemini have no error flag: the adapter prefixes the text (e.g. `"Error: …"`) or uses Gemini's `error` key. **Unverified** whether OpenAI has an error field on `function_call_output`.

### 4.4 JSON Schema dialects for tool parameters

| | Anthropic (`strict:true` / structured outputs) | OpenAI (`strict:true`) | Gemini |
|---|---|---|---|
| Entry point | `input_schema`, plus optional `"strict": true` per tool ([structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)) | `parameters` + `strict` ([function calling](https://developers.openai.com/api/docs/guides/function-calling)) | `parameters` (OpenAPI 3.0 `Schema` subset) **or** `parametersJsonSchema` (JSON Schema, mutually exclusive) ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
| Closure | strict: `additionalProperties:false` required on every object | `additionalProperties:false` on every object; "All fields in `properties` must be marked as `required`"; optional = add `null` to `type` | `additionalProperties`, `required`, `propertyOrdering` accepted (example in the `parametersJsonSchema` doc comment) |
| Supported (strict) | basic types, `enum`, `const`, `anyOf`/`allOf` (limited), `$ref`/`$defs`, `default`, formats (`date-time`, `email`, `uri`, `uuid`, …), `minItems` 0/1 | subset per [structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs); exhaustive list **Unverified** | OpenAPI subset: `type, format, description, nullable, enum, items, properties, required, propertyOrdering, minItems, maxItems` ([generate-content API](https://ai.google.dev/api/generate-content)) |
| Not supported (strict) | recursive schemas, external `$ref`, `minimum`/`maximum`/`multipleOf`, `minLength`/`maxLength`, `pattern` | **Unverified** list | `oneOf`/`$ref` on the OpenAPI path (**Unverified**); JSON Schema path limits "very large or deeply nested" (**Unverified**) |

- MCP Connector tools arrive as arbitrary JSON Schema. The adapters therefore need a **schema-narrowing pass**, computed once per tool list and cached by `tools_hash` so it stays byte-stable for caching (context.md §5.2): Anthropic sends as-is (non-strict), OpenAI sends non-strict unless the schema already qualifies, Gemini uses `parametersJsonSchema` (avoids the OpenAPI conversion).
- ⚠ cross-spec: agent-loop.md's `ToolDef.Schema json.RawMessage` is one schema. That is fine as input. Just note that "strict" is a per-Provider, per-tool derived property, not a ToolDef field. Rec: don't use strict by default for Connector tools (their schemas often use `pattern`/`minLength`). Built-in tools can be written strict-compatible and opt in. See Q3.

### 4.5 `tool_choice`

| | Anthropic | OpenAI Responses | Gemini `toolConfig.functionCallingConfig` |
|---|---|---|---|
| Values | `{type:auto\|any\|tool(name)\|none}` + `disable_parallel_tool_use` ([Messages API](https://platform.claude.com/docs/en/api/messages)) | `"auto"\|"required"\|"none"\|{type:"function",name}\|{type:"allowed_tools",mode,tools}` ([function calling](https://developers.openai.com/api/docs/guides/function-calling)) | `mode: AUTO\|ANY\|NONE\|VALIDATED` + `allowedFunctionNames` (ANY/VALIDATED only) ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |

- **Opus 5.5, Fable 5.1 and Mythos 5.1 reject forced tool use**: `any`/`tool` → 400 "tool_choice: type "tool" and "any" are not supported for this model". This includes the token-counting endpoint. Anthropic's advice is `auto` + strict tool use, or structured outputs ([errors](https://platform.claude.com/docs/en/api/errors)).
- Neutral `ToolChoice{Mode: auto|none|required|one, Name}`. `required`/`one` on a model whose catalog entry has `forced_tool_choice=false` is a caller error, caught in the adapter before sending. Base-version loop turns use only `auto` (agent-loop.md). The Approver's small-LLM fallback and Tidy should use structured output (§10), not forced tools.
- The Gemini docs page now shows an `allowed_tools`/`call_id` shape that doesn't match `generateContent`'s `toolConfig` ([function calling](https://ai.google.dev/gemini-api/docs/function-calling)). It looks like a different (Interactions-style) API surface. **Unverified**: trust the go-genai `generateContent` types.

### 4.6 Stop/finish reasons (full enums)

| Neutral `StopReason` (fixed) | Anthropic `stop_reason` ([anthropic-sdk-go message.go](https://github.com/anthropics/anthropic-sdk-go/blob/main/message.go)) | OpenAI Responses `status` / `incomplete_details.reason` ([response.go](https://github.com/openai/openai-go/blob/main/responses/response.go)) | Gemini `finishReason` ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
|---|---|---|---|
| `end_turn` | `end_turn`, `stop_sequence` | `completed`, no function_call | `STOP` |
| `tool_use` | `tool_use` | `completed` with function_call items | `STOP` with functionCall parts |
| `max_tokens` | `max_tokens` | `incomplete` + `max_output_tokens` | `MAX_TOKENS` |
| `refusal` | `refusal` | `refusal` content part; `incomplete` + `content_filter` | `SAFETY`, `RECITATION`, `LANGUAGE`, `BLOCKLIST`, `PROHIBITED_CONTENT`, `SPII`, `IMAGE_*`; also `promptFeedback.blockReason` |
| `context_exceeded` | `model_context_window_exceeded` | upfront 400 (agent-loop.md Decision 20) | **Unverified** (likely a 400) |
| `malformed_call` | — | — | `MALFORMED_FUNCTION_CALL`, `UNEXPECTED_TOOL_CALL`, `TOO_MANY_TOOL_CALLS` |
| `other` | `pause_turn` (server tools only) | `failed`, `cancelled`, `incomplete` + `max_messages` / `steered` (**Unverified** meaning) | `OTHER`, `FINISH_REASON_UNSPECIFIED`, `NO_IMAGE` |

- ⚠ cross-spec: agent-loop.md §5.3 lists Anthropic `model_context_window_exceeded` under the *error* table. In the SDK it is a **`stop_reason`** on a successful response, not an HTTP error. The adapter maps it to `StopReason=context_exceeded`, and the loop routes it to Compaction Tier 2 the same way. The behaviour is the same, but the classification is in the wrong table.
- ⚠ cross-spec: agent-loop research §3.1 cites Gemini `MISSING_THOUGHT_SIGNATURE`, which is **not** in go-genai's `FinishReason` enum today. It may be an error message rather than a finish reason. **Unverified**. Map any unknown enum value to `other` + log.
- OpenAI `status: queued` exists (background mode). We never use background mode.

### 4.7 Other request quirks worth encoding once

- Anthropic 4.7+ models reject `thinking.type:"enabled"` (use `adaptive` + `output_config.effort`). Fable 5.1/Opus 5.5/Mythos reject `thinking.type:"disabled"` (thinking is always on; `display:"omitted"` hides it) ([errors](https://platform.claude.com/docs/en/api/errors)). This belongs in the catalog as `thinking: adaptive_only | always_on`, not in the loop.
- Anthropic prefix binding and `drop_block`: already decided (context.md §5.5). The 400 message names `thinking.block_binding.prefix_mismatch_behavior` ([errors](https://platform.claude.com/docs/en/api/errors)). The adapter must classify that 400 as a **bug** (our projection failed to drop a block), not a key/billing error. See Q9.

---

## 5. Streaming

### 5.1 Per-provider stream shapes

- **Anthropic** (SSE, typed events): `message_start` (input usage), `content_block_start`, `content_block_delta` (`text_delta`, `input_json_delta.partial_json`, `thinking_delta`, `signature_delta`, `citations_delta`), `content_block_stop`, `message_delta` (`stop_reason`, cumulative `usage`), `message_stop`, `ping`, `error` ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)). Go: `client.Messages.NewStreaming` + `message.Accumulate(event)` ([errors](https://platform.claude.com/docs/en/api/errors)).
- **OpenAI Responses** (SSE, typed events; each has `sequence_number`): lifecycle `response.created/queued/in_progress/completed/incomplete/failed`, items `response.output_item.added/done`, `response.content_part.added/done`, text `response.output_text.delta/done`, `response.output_text.annotation.added`, tool args `response.function_call_arguments.delta/done`, reasoning `response.reasoning_summary_part.*`, `response.reasoning_summary_text.delta/done`, `response.reasoning_text.delta/done`, `response.refusal.delta/done`, per-built-in-tool events (`web_search_call.*`, `mcp_call.*`, …), and `error` ([openai-go response.go `ResponseStreamEventUnion`](https://github.com/openai/openai-go/blob/main/responses/response.go), [streaming](https://developers.openai.com/api/docs/guides/streaming-responses)). Usage arrives only on the final `response` object.
- **Gemini**: `streamGenerateContent` yields successive `GenerateContentResponse` chunks, not typed deltas ([generate-content API](https://ai.google.dev/api/generate-content)). Go: `Models.GenerateContentStream(ctx, model, contents, cfg) iter.Seq2[*GenerateContentResponse, error]` ([models.go](https://github.com/googleapis/go-genai/blob/main/models.go)). Each chunk's parts are new content to append, and function calls arrive whole: `partialArgs`/`streamFunctionCallArguments` are "not supported in Gemini API" ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)). `usageMetadata` appears on chunks. **Unverified** whether it is cumulative on every chunk or only on the last.

### 5.2 Neutral `Delta`

⚠ cross-spec: agent-loop.md §4.1 and event-log.md §5.11 fix `Delta{TurnID, Idx, Text}` (text only). The details toggle in product.md ("full trace and thinking") needs thinking deltas, and tool-card previews need tool-arg deltas. Additive extension:

```go
type DeltaKind uint8 // 0 = text (zero value keeps today's callers correct), thinking, tool_args, tool_start

type Delta struct {
    TurnID string
    Idx    int       // neutral part index within the assistant message
    Kind   DeltaKind
    Text   string    // text / thinking / partial tool-args JSON fragment
    CallID string    // Kind=tool_start|tool_args: our ToolUse.ID, so parallel calls stay separate
    Name   string    // Kind=tool_start: tool name, for the card title
}
```

| Neutral | Anthropic | OpenAI | Gemini |
|---|---|---|---|
| text | `text_delta` | `output_text.delta` | new `text` part (non-thought) |
| thinking | `thinking_delta` | `reasoning_summary_text.delta` (summaries only) | `text` part with `thought:true` |
| tool_start | `content_block_start{tool_use}` | `output_item.added{function_call}` | whole `functionCall` part (start + full args at once) |
| tool_args | `input_json_delta` | `function_call_arguments.delta` | — (synthesize one delta with the full args) |

- **No usage delta.** Usage is authoritative only on the final `Response`. Only Anthropic streams a cumulative count usefully, and nothing durable should read a delta (event-log.md §5.11: deltas are ephemeral). A UI token counter can come from `usage.recorded`.
- `Idx` must be the **neutral** part index. OpenAI's `output_index`/`content_index` and Anthropic's block `index` differ (OpenAI splits a message into items), so the adapter maps them.
- Signature/opaque deltas are never emitted. They are accumulated into the final `Response` only.
- Execution still uses only the final accumulated args (agent-loop.md §6).

### 5.3 Mid-stream errors

- Anthropic: "an error can occur after the API returns a 200 response… See Error events" ([errors](https://platform.claude.com/docs/en/api/errors)). The `error` event carries the same `{type, message}` shape (e.g. `overloaded_error`) ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)).
- OpenAI: `error` event and terminal `response.failed` (with `response.error`) ([streaming](https://developers.openai.com/api/docs/guides/streaming-responses)).
- Gemini: no in-band error event documented. The iterator yields `(nil, err)` (the `iter.Seq2` signature). **Unverified** whether a 200 stream can end with an error payload.
- Adapter rule: any mid-stream failure discards the partial message. "Tool use and extended thinking blocks cannot be partially recovered" ([streaming](https://platform.claude.com/docs/en/build-with-claude/streaming)). The adapter returns `*llm.Error{Kind}` with the in-band type mapped exactly as the HTTP status would be (`overloaded_error` → Transient). Partial text is never returned as a `Response`. This fits agent-loop.md §5.3's "stream drop" row.

### 5.4 Stream idle timeout

- No Provider documents an idle timeout value. Anthropic warns "Some networks may drop idle connections", recommends TCP keep-alive (its SDKs set it), and sends `ping` events ([errors — long requests](https://platform.claude.com/docs/en/api/errors)).
- Rec: an adapter-level watchdog that resets on every received event (including `ping`), so no bytes for N seconds → cancel with cause `ErrStreamIdle` → Transient. Codex uses 300 s ([model-provider-info](https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs)). Adaptive thinking can be silent for a long time, but Anthropic still pings. Gemini sends nothing between chunks (**Unverified**), so its idle window needs to be generous. See Q4.
- Set no overall request timeout on streams (turns can legitimately run for many minutes). Use `ctx` + idle watchdog only.

---

## 6. Usage & cost

### 6.1 Usage fields

| | Anthropic `usage` ([message.go](https://github.com/anthropics/anthropic-sdk-go/blob/main/message.go)) | OpenAI Responses `usage` ([response.go](https://github.com/openai/openai-go/blob/main/responses/response.go)) | Gemini `usageMetadata` ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go)) |
|---|---|---|---|
| Input | `input_tokens`: **uncached tail only**; total = `input + cache_read + cache_creation` ([prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)) | `input_tokens` (total, includes cached) | `promptTokenCount` (includes cached) |
| Cache read | `cache_read_input_tokens` | `input_tokens_details.cached_tokens` | `cachedContentTokenCount` (+ `cacheTokensDetails` by modality) |
| Cache write | `cache_creation_input_tokens` + `cache_creation.{ephemeral_5m_input_tokens, ephemeral_1h_input_tokens}` | `input_tokens_details.cache_write_tokens` (new; pricing semantics **Unverified**) | — (implicit caching) |
| Output | `output_tokens` (inclusive, "authoritative total used for billing") | `output_tokens` (inclusive) | `candidatesTokenCount` (**Unverified** whether it includes thoughts; likely not) |
| Reasoning | `output_tokens_details.thinking_tokens` ("Always ≤ `output_tokens`") | `output_tokens_details.reasoning_tokens` | `thoughtsTokenCount` |
| Server tools | `server_tool_use.{web_search_requests, web_fetch_requests}` | per built-in call item (**Unverified** aggregated counter) | `toolUsePromptTokenCount` |
| Other | `service_tier`, `inference_geo` | `total_tokens` | `totalTokenCount`, `trafficType` |

- Neutral `Usage{Input, CacheRead, CacheWrite5m, CacheWrite1h, Output, Reasoning, ServerTools map[string]int}` with **`Input` normalized to "uncached, non-written input"**, so the three add up the same way. Adapters subtract (OpenAI `input - cached - cache_write`, Gemini `prompt - cached`). This goes to `usage.recorded` as one row per unit (`quantity`/`unit`/`cost_micros`, event-log.md §3). Deciding whether it is one event with several quantities or several events is event-log's call. See Q6.
- Gemini's `thoughtsTokenCount` is billed as output (**Unverified**, pricing page), so cost = `(candidates + thoughts) × output price`.

### 6.2 Cost computation

- No Provider exposes a pricing API. Prices exist only as docs pages: [Anthropic pricing](https://platform.claude.com/docs/en/about-claude/pricing), OpenAI pricing (**Unverified** URL, `developers.openai.com/api/docs/pricing`), [Gemini pricing](https://ai.google.dev/gemini-api/docs/pricing) (**Unverified**, not fetched). None of the models-list endpoints return prices (§7).
- OpenAI has an org **Costs API** (`GET /v1/organization/costs`, admin key, daily historical spend) ([costs reference](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs)). It is useless for BYOK: we hold a project key, not an admin key, and it reports after the fact.
- So: `cost_micros = Σ tokens_by_class × price_by_class(model, tier)` from a **static price table** versioned in the repo (§7.3). Price-bearing dimensions to keep: input, cache read, cache write 5m/1h, output, per-server-tool fees, long-context tiers (**Unverified** which current models have >200k tiers), batch/priority tiers (not used).
- Store the dollar amount **at record time** in `usage.recorded` (already the design). A later price-table fix must never rewrite history. The Budget reads `cost_micros` (event-log.md §5.9).
- Label dollar figures as "estimated". The user's Provider invoice is the truth under BYOK (ADR 0006).

---

## 7. Model catalog

### 7.1 What the list endpoints return

- **Anthropic `GET /v1/models`**: `id`, `display_name`, `created_at`, `max_input_tokens`, `max_tokens`, and a `capabilities` object (`batch`, `citations`, `code_execution`, `context_management`, `effort`, `image_input`, `pdf_input`, `structured_outputs`, `thinking.types`) ([models list](https://platform.claude.com/docs/en/api/models/list)). No price.
- **OpenAI `GET /v1/models`**: `id`, `object`, `created`, `owned_by` (plus `shutdown_date` per a fork's read, **Unverified**) ([models list](https://developers.openai.com/api/docs/api-reference/models/list)). No limits, capabilities or price.
- **Gemini `models.list`**: `name`, `baseModelId`, `version`, `displayName`, `description`, `inputTokenLimit`, `outputTokenLimit`, `supportedGenerationMethods`, `thinking`, `temperature`/`maxTemperature`/`topP`/`topK` ([models API](https://ai.google.dev/api/models)). No capability flags beyond thinking, and no price.

### 7.2 Live vs hardcoded

| Field | Anthropic | OpenAI | Gemini |
|---|---|---|---|
| context window, max output | live | hardcode | live |
| thinking mode (adaptive/always-on/budget) | live (partial) | hardcode | live bool + hardcode mode |
| vision / PDF | live | hardcode | hardcode |
| structured outputs | live | hardcode | hardcode |
| forced tool_choice allowed | hardcode (§4.5) | hardcode | hardcode |
| parallel tools, prompt-cache min tokens, cache TTLs | hardcode | hardcode | hardcode |
| price | hardcode | hardcode | hardcode |

- The list endpoints are also **per-key**: they show what *this user's key* can call. That is useful for the `/model` picker (§8.1).

### 7.3 Maintaining it

- Prior art: LiteLLM keeps one community-maintained `model_prices_and_context_window.json` and can auto-sync it from GitHub ([file](https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json), [sync docs](https://docs.litellm.ai/docs/proxy/sync_models_github)). Vercel/Pydantic keep model ids in code (**Unverified** whether they keep capability tables).
- Rec: `internal/llm/catalog/models.json`, embedded with `go:embed`, reviewed like code. One entry per model id carries: limits, capability flags, price table, `source_url`, `verified_at`. At Worker start and daily, merge live fields (Anthropic, Gemini). On a conflict, prefer live limits and log a diff. An unknown id is allowed with conservative defaults and **no dollar amount**, and is flagged in the UI ("price unknown").
- ⚠ cross-spec: agent-loop.md's `Models() []ModelInfo` has no `ctx`/`error` and no key. A live per-key list needs one: `ListModels(ctx) ([]ModelInfo, error)` with the key already bound to the client. Rec: keep `Models()` as the static catalog view and add the live, per-key call on a separate adapter method used only by settings and `/model`. See Q7.
- Optional (not merge-blocking): a scheduled CI job that diffs the live lists against the table.

---

## 8. Keys & errors

### 8.1 BYOK key validation

| Provider | Call | Why |
|---|---|---|
| Anthropic | `GET /v1/models` (auth only); optionally `POST /v1/messages/count_tokens`, documented as "free to use" with its own rate limit ([token counting](https://platform.claude.com/docs/en/build-with-claude/token-counting)) | 401 = bad key, 403 = no permission. Neither proves billing works |
| OpenAI | `GET /v1/models` | 401 = bad key. Billing/quota is not checked (**Unverified** whether the list is ever billed; it does no inference) |
| Gemini | `GET /v1beta/models` (`Models.List`) | 401/403. **Unverified** whether `countTokens` is free |

- Billing state (402, spend cap, `insufficient_quota`) only shows on a real generation call. Rec: validate with the list call when the key is saved (free, and it also fills the per-key `/model` picker). Don't spend the user's money on a probe. The first real turn surfaces billing problems through the normal terminal-error path (agent-loop.md §5.3 key/billing row). See Q8.
- The validation runs in the Worker (or a Worker-role job), because that is the only place the key is decrypted (product.md). The API role never calls the Provider with a user key.

### 8.2 Error shapes and classes

| Class (agent-loop.md §5.3) | Anthropic ([errors](https://platform.claude.com/docs/en/api/errors), [rate limits](https://platform.claude.com/docs/en/api/rate-limits)) | OpenAI ([error codes](https://developers.openai.com/api/docs/guides/error-codes)) | Gemini ([API errors](https://ai.google.dev/gemini-api/docs/api-errors)) |
|---|---|---|---|
| shape | `{"type":"error","error":{"type","message"},"request_id"}`; `request-id` header | `{"error":{"message","type","param","code"}}` (**Unverified**); `x-request-id` header (**Unverified**) | `{"error":{"code","message","status"}}` (Google standard, **Unverified**) |
| bad key | 401 `authentication_error`; 403 `permission_error` | 401 (`invalid_api_key`, **Unverified**) | 401 `authentication`; 403 `permission_denied` |
| billing | 402 `billing_error`; **400 `invalid_request_error` when the user's own org/workspace spend limit is hit** | 429 `insufficient_quota` (**Unverified**) | 402 `payment_required` ("Prepay credit balance is depleted") |
| spend cap | 429 tier spend cap: **no `retry-after`**, keeps failing | 429 spend/usage-limit codes (**Unverified**) | 429 `quota_exceeded` ("Exceeded daily quota") |
| transient | 429 `rate_limit_error` (with `retry-after`), 500 `api_error`, 504 `timeout_error`, 529 `overloaded_error` | 429 rate limit, 5xx | 429 `rate_limit_exceeded`/`too_many_requests`, 500 `api_error`, 503 `service_unavailable`, 504 `deadline_exceeded` |
| window | stop_reason `model_context_window_exceeded` (§4.6); 413 `request_too_large` is size, not tokens | 400 `context_length_exceeded` (fixed in agent-loop.md) | **Unverified** (400 `INVALID_ARGUMENT`?) |

- ⚠ cross-spec: agent-loop.md §5.3 maps "400 invalid" to Key/billing. For Anthropic, a spend-limit 400 and a malformed-request 400 are both `invalid_request_error`. They differ only in the message. Both are terminal, so behaviour is right, but the UI text ("fix your key") is wrong for a projection bug (e.g. prefix-binding 400, §4.7). Rec: add a `Kind: Bug` (terminal, "internal error", logged with `request_id`) separate from `KeyBilling`. See Q9.
- A 413 (request too large) is not a window problem. It means inline media is too big (§9). Classify it as terminal-bug unless the projection can move media to a Files API.
- The fork drafts disagreed on the Gemini billing code. The Gemini errors page lists 402 `payment_required`. Another draft cited 400 `FAILED_PRECONDITION` for "billing not enabled" (**Unverified**). Treat both as KeyBilling.
- Always capture the request id (`Response.RequestID`, already in agent-loop.md) and put it on `session.error` for support.

### 8.3 Rate-limit headers

- Anthropic: `retry-after`, `anthropic-ratelimit-{requests,tokens,input-tokens,output-tokens}-{limit,remaining,reset}`, plus priority-tier variants. Cache-read tokens don't count toward ITPM for most models ([rate limits](https://platform.claude.com/docs/en/api/rate-limits)).
- OpenAI: `x-ratelimit-{limit,remaining,reset}-{requests,tokens}` (+ project-scoped variants), `retry-after` ([rate limits](https://developers.openai.com/api/docs/guides/rate-limits)).
- Gemini: no rate-limit headers documented ([API errors](https://ai.google.dev/gemini-api/docs/api-errors) doesn't mention any). Use `retry-after` if present, or `google.rpc.RetryInfo` in the error details (as Gemini CLI does, agent-loop research §3.6; **Unverified** from Gemini docs).
- Use: the retry decorator reads `retry-after` (≤60 s in-process, >60 s → `timer.set`, agent-loop.md §5.3). `remaining` headers can go to OTel only. There is no proactive throttling in the base version.

---

## 9. Uploads and files

| | Anthropic | OpenAI | Gemini |
|---|---|---|---|
| Inline ceiling | whole Messages request **32 MB** (413 beyond) ([errors](https://platform.claude.com/docs/en/api/errors)) | **Unverified** (a fork cited 20 MB per base64 image, secondary source) | use the Files API when the total request exceeds **100 MB** ([Gemini files](https://ai.google.dev/gemini-api/docs/files)) |
| Files API size | 500 MB/file, 1 TB/org ([files](https://platform.claude.com/docs/en/build-with-claude/files)) | 512 MB/file, 2.5 TB/project (**Unverified**, [files create](https://developers.openai.com/api/reference/resources/files/methods/create)) | 2 GB/file (50 MB for PDFs), 20 GB/project ([Gemini files](https://ai.google.dev/gemini-api/docs/files)) |
| Retention | until deleted, or `expires_at` 1 h–90 d ([files](https://platform.claude.com/docs/en/build-with-claude/files)) | until deleted, or `expires_after` 1 h–30 d (**Unverified**) | **48 hours**, fixed ([Gemini files](https://ai.google.dev/gemini-api/docs/files)) |
| Cost | file ops free; content billed as input tokens | **Unverified** | free |
| Reference | `source:{type:"file", file_id}` | `file_id` in `input_image`/`input_file` | `fileData:{fileUri, mimeType}` |
| Scope | workspace-scoped: "Never accept `file_id` values from end users or other untrusted sources" ([files](https://platform.claude.com/docs/en/build-with-claude/files)) | project-scoped (**Unverified**) | project-scoped |

- The neutral part always carries our own `BlobRef` (object storage, event-log.md §3). **Never a Provider file id.** Provider file ids are per key, expire, and would break `/model` switching and replay.
- Adapter options per turn:
  - (a) **inline**: read the blob, base64 it, send it. No Provider-side state, and replay is exact. The cost is resending bytes every turn. Anthropic caches it, since images/documents sit inside the cached prefix (context.md §5.2), so token cost is cached. Bandwidth is not.
  - (b) **lazy upload**: upload once per (blob sha256, Provider, key fingerprint), keep `file_id` + expiry in a Worker-side cache table (not in the Event Log), re-upload after expiry or key rotation. More state, and a Provider-side copy of user data outlives our hard delete unless we delete it too.
- Hard delete (event-log.md §5.13) must also delete Provider-side copies if (b) is used. ⚠ cross-spec: the cleanup job doesn't list this today.
- Rec: (a) by default. Use (b) only when a request would exceed the inline ceiling. Start with Anthropic/Gemini only, and treat (b) as later work. See Q5.
- Media also needs per-Provider MIME checks (e.g. which image types each accepts). Put them in the catalog and reject unsupported types at upload, not at turn time. **Unverified** exact lists.

---

## 10. Structured output / JSON mode

| | Anthropic | OpenAI | Gemini |
|---|---|---|---|
| Mechanism | **`output_config.format`** (JSON schema), GA, no beta header. The old `output_format` is deprecated ([structured outputs](https://platform.claude.com/docs/en/build-with-claude/structured-outputs)) | Responses `text.format: {type:"json_schema", name, schema, strict:true}` (Chat: `response_format`) ([structured outputs](https://developers.openai.com/api/docs/guides/structured-outputs)); legacy `json_object` gives valid JSON only | `generationConfig.responseMimeType:"application/json"` + `responseSchema` (OpenAPI subset) **or** `responseJsonSchema` ([types.go](https://github.com/googleapis/go-genai/blob/main/types.go), [structured output](https://ai.google.dev/gemini-api/docs/structured-output)) |
| Tool-args variant | `strict:true` on a tool | `strict:true` on a function | `VALIDATED` mode (§4.5) (**Unverified** semantics beyond the enum comment) |
| Schema limits | same as §4.4 (no recursion, no numeric/length/`pattern`) | same as §4.4 | same as §4.4 |
| Model support | list on the page (Opus 4.5+, Sonnet 4.5+, Haiku 4.5, Opus 5.x, Fable, Mythos) | **Unverified** list | **Unverified** list |

- **Tidy** (the memory System Session) and the Approver's small-LLM fallback (ADR 0004) are internal features that need a fixed JSON shape. This doc only notes the mechanism. Because they run on the user's key and whatever model the Default Agent uses, the neutral `Request` needs a `ResponseSchema json.RawMessage` field that each adapter maps as above, not forced tool use (which fails on Opus 5.5/Fable 5.1, §4.5).
- **Jev** is a Platform Service model from TypeSafe (CONTEXT.md). It is not called through a user's Provider, so this section does not apply to it. **Unverified**: how Jev is invoked is out of this doc's scope.
- Always validate the returned JSON against the schema in Go (e.g. a JSON Schema validator), even with strict mode, and treat failure as `malformed_call` (retry once, agent-loop.md stop-reason table).
- Author internal schemas in the intersection dialect (closed objects, all required, nullable for optional, no numeric/length/pattern, no recursion). Then one schema works on all three.

---

## 11. Build vs adopt

### 11.1 Official Go SDKs (ADR 0002's path)

| Repo | Stars | Last push | Notes |
|---|---|---|---|
| [anthropics/anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) | ~1.2k | 2026-09-22 | GA; v1.75.0 at a fork's check; `Messages.NewStreaming` + `Accumulate`; `option.WithMaxRetries(0)` |
| [openai/openai-go](https://github.com/openai/openai-go) | ~3.5k | 2026-09-26 | GA; v3.x; separate `responses` and `chat` packages; Go 1.25+ for recent versions (per a fork's README read) |
| [googleapis/go-genai](https://github.com/googleapis/go-genai) | ~1.2k | 2026-09-26 | GA; `iter.Seq2` streaming; README advises pinning `< 2.0.0` (per a fork's read). Replaces the deprecated `generative-ai-go` ([Vertex deprecation](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/deprecations/genai-vertexai-sdk)) |

All three are first-party and active. They are typed HTTP clients, with no message model forced on us, so they sit behind our adapters without leaking.

### 11.2 Go multi-provider libraries

| Repo | Stars | Last push | Kind | Fit |
|---|---|---|---|---|
| [cloudwego/eino](https://github.com/cloudwego/eino) | ~13.2k | 2026-09-24 | full LLM app framework (ByteDance) with its own `schema.Message` | adopting it means adopting its message model: contradicts ADR 0002 |
| [tmc/langchaingo](https://github.com/tmc/langchaingo) | ~9.7k | 2026-01-11 (stale) | LangChain port, own message types | same, and stale |
| [sashabaranov/go-openai](https://github.com/sashabaranov/go-openai) | ~10.8k | 2026-09-22 | single Provider (OpenAI), community | no reason over the official SDK |
| long tail (`lexlapax/go-llms`, `aholstenson/llms-go`, …) | <1k | varied | small unified clients | too immature for core infrastructure (**Unverified**, not vetted) |

None of them models `opaque` thinking replay, prefix binding, or Gemini signatures the way agent-loop.md needs, and every one brings its own message type. That is exactly what ADR 0002 rejected.

### 11.3 HTTP gateway in front

- **LiteLLM proxy** ([BerriAI/litellm](https://github.com/BerriAI/litellm), ~59.7k★, very active): a Python service with an OpenAI-shaped API across 100+ Providers.
  - It adds a second runtime next to "a Go backend (single binary)" (product.md).
  - It makes the OpenAI Chat shape our effective wire format (ADR 0002).
  - ⚠ cross-spec: to call Providers with a user's key it must receive the plaintext key, which means a process other than the Worker holds decrypted Provider Keys (product.md: "decrypted only in the worker").
- **OpenRouter**: a hosted router. BYOK mode stores the user's Provider keys **encrypted on OpenRouter** and charges a **5% fee** on top of the Provider price ([OpenRouter BYOK](https://openrouter.ai/docs/guides/overview/auth/byok)). ⚠ cross-spec: that breaks ADR 0006 ("Users pay providers directly") in spirit and product.md's key-custody rule in letter.
- Neither fits. A proxy also hides Provider-specific fields (signatures, `encrypted_content`, cache fields) unless it passes them through verbatim, which puts the thinking-replay rules at risk.

### 11.4 Verdict

- Official SDKs behind our own adapters is the only option compatible with ADR 0002, ADR 0006 and product.md's key custody without caveats. The research confirms it rather than reopening it.
- Borrow, don't depend: LangChain's escape-hatch block, Vercel's `providerMetadata`, LiteLLM's JSON price table as a *format* reference (**Unverified** licence for copying its data; MIT per repo, check before vendoring).

---

## 12. Testing

Stays consistent with agent-loop.md §7.4 and §11. The **fake Provider** (`fake.New(fake.ToolUse(…), fake.Err(…), fake.Text(…), fake.BlockUntilCancel())`) sits at the `Provider` interface and covers the loop. Nothing here changes it, except that it should also be able to script `Delta` kinds (§5.2). Everything below is *adapter* testing:

- **Request goldens** (per adapter): table of `llm.Request` → golden JSON of the exact HTTP body the SDK would send, captured via an `http.RoundTripper` that records and returns a canned response. Cover: system + three breakpoints, thinking replay same-Provider vs foreign (dropped; Gemini dummy signature), parallel results in tool_use order, multi-part tool results, the schema-narrowing pass (§4.4), `ToolChoice` on a forced-tool-rejecting model (expect a local error), structured output, and id rewriting for foreign history.
- **Response/stream goldens**: raw SSE bytes (Anthropic, OpenAI) or JSON chunk arrays (Gemini) in `testdata/`, fed through the real SDK decoder via the fake transport. Assert (a) the `Delta` sequence, (b) the final `msg.Message`/`StopReason`/`Usage` golden, (c) `Usage` normalization arithmetic (§6.1).
- **Error fixtures**: each row of §8.2 as a recorded response (status, headers, body) → expected `llm.Error{Kind, RetryAfter, RequestID}`. Include: spend-cap 429 without `retry-after`, 400 spend limit, mid-stream `error` after 200, `response.failed`, idle stall (a transport that stops sending, then the watchdog fires).
- **Record/replay cassettes**: product.md commits to cassettes of real sessions. At the adapter level use [dnaeon/go-vcr](https://github.com/dnaeon/go-vcr) (~1.4k★, active) or our own RoundTripper recorder. **Scrub `x-api-key`, `authorization`, `x-goog-api-key` before writing**, because secrets never enter fixtures (event-log.md payload rule, applied to test data). Replay in CI. Re-record manually with `JF_RECORD=1` + env keys.
- **Round-trip property test**: `msg → wire → msg` for same-Provider messages must be lossless, including `Opaque` bytes, byte for byte.
- **Upcaster goldens**: each historical `msg_v` fixture upcasts to the latest (event-log.md §9 style).
- **Live contract suite** (opt-in, not CI-blocking): one tiny real call per Provider and model family, run on demand next to the eval suite. It catches Provider drift (new enum values, renamed fields) that recorded fixtures can't.
- **Catalog drift job**: the §7.3 diff, informational.

---

## 13. Options

| | A. Minimal neutral format, official SDKs | B. Rich neutral format + escape hatch + catalog, official SDKs (recommended) | C. Adopt a library or proxy |
|---|---|---|---|
| `msg.Part` | text, image, tool_use, tool_result, thinking only | §3.1 set: + `FilePart`, citations, multi-part tool results, `NativePart` escape hatch, `msg_v` | the library's/proxy's message type |
| Streaming `Delta` | text only (as today) | + thinking / tool_start / tool_args (additive) | whatever the library exposes |
| Model data | hardcoded `Models()` | embedded catalog + live merge + per-key list | library-maintained |
| ADR 0002 | yes | yes | **no** |
| ADR 0006 / key custody | yes | yes | LiteLLM/OpenRouter: ⚠ no; eino/langchaingo: yes |
| Cost of a new Provider block type later | new `Kind` + upcaster + 3 adapters | often zero (`NativePart`), else a new `Kind` | wait for upstream |
| Cost now | lowest | moderate: more adapter branches, more fixtures | lowest code, highest coupling |
| Learning value (product goal) | medium | high | low |

**Recommend B**, built in this order: (1) `msg` types + `msg_v` + upcaster skeleton, (2) Anthropic adapter (richest features, and the one the Fable/Opus rules bite), (3) OpenAI Responses, (4) Gemini, (5) catalog + price table, (6) Files API path only when a user hits the inline ceiling. A is fine if we accept a migration the first time citations or multi-part tool results are needed. Given MCP tools already return images, that day is close. C is ruled out by ADR 0002 and key custody.

---

## 14. Open questions for grilling

- **Q1. How is the neutral format versioned inside payloads?** event-log.md says "the neutral message format carries its own version inside the payload (ADR 0002)"; ADR 0002 doesn't say it. Proposed: add `msg_v` (int) to every payload that embeds `msg`, with upcasters in `internal/msg`. A new `Part.Kind` is additive (no bump). A Worker that sees an unknown `msg_v` or `Kind` releases the Lease (event-log.md §5.19). Add one line to ADR 0002 so the two docs agree.
- **Q2. Is `tool.Result.Content` a single `msg.Part` or `[]msg.Part`?** All three Providers accept multi-part tool results (text + image). Proposed: `[]msg.Part` (inside `ToolResultPart.Parts`). Structured JSON is a `TextPart`, which the Gemini adapter wraps as `{"output": …}`. This needs an edit to agent-loop.md §4.2.
- **Q3. Strict tool schemas by default?** Proposed: no for Connector tools (arbitrary JSON Schema, often uses `pattern`/`minLength`). Yes, opt-in, for built-in tools written in the intersection dialect. Gemini always uses `parametersJsonSchema`. The narrowing pass is cached per `tools_hash` so the prefix stays byte-stable.
- **Q4. Stream idle timeout value?** No Provider documents one. Proposed: a watchdog that resets on any event, including Anthropic `ping`. 300 s for all three to start (Codex precedent). On firing, cancel with `ErrStreamIdle` → Transient. Tune with the eval suite.
- **Q5. Inline base64 or Provider Files API for Uploads?** Proposed: inline always in the base version (exact replay, no Provider-side copies, `/model`-safe). Add lazy Files API upload later, only for requests over the inline ceiling (Anthropic 32 MB, Gemini 100 MB). If added, cache `file_id` per (sha256, Provider, key fingerprint) outside the Event Log and extend hard delete to delete Provider copies.
- **Q6. How does normalized `Usage` land in `usage.recorded`?** Proposed: the adapter returns normalized `Usage` (uncached input, cache read, cache write 5m/1h, output, reasoning, server-tool counts). `exec` writes one `usage.recorded` per non-zero class (`kind=llm`, `unit=input_tokens|cache_read_tokens|…`), with `cost_micros` from the catalog at record time. The Budget sums `cost_micros`. Check this against event-log.md's one-row-per-event `UNIQUE (session_id, seq)`: several classes need several events or a multi-quantity payload. Event-log decides.
- **Q7. Where does the per-key live model list live, given `Models()` has no ctx?** Proposed: keep `Models() []ModelInfo` as the static catalog view (loop and Compaction thresholds). Add `ListModels(ctx) ([]ModelInfo, error)` on the adapter for settings and the `/model` picker, running in a Worker-role job because it needs the decrypted key. Needs a nod in agent-loop.md §4.1.
- **Q8. How do we validate a Provider Key without spending money?** Proposed: the models-list call when the key is saved (free, and it fills the picker). Billing problems surface on the first real turn as terminal KeyBilling errors. No paid probe. Confirm with a live bad-key and no-credit test per Provider before launch, because several OpenAI/Gemini codes are **Unverified**.
- **Q9. Split `Bug` from `KeyBilling` in the error classes?** Anthropic returns 400 `invalid_request_error` both for "your spend limit" and for our own projection bugs (prefix-binding, malformed thinking replay). Proposed: add `Kind: Bug` (terminal, logs `request_id`, UI says "internal error", no "fix your key" hint). Classify by known message markers (`block_binding`, `thinking`, `tool_choice`). Needs a row in agent-loop.md §5.3.
- **Q10. Is `model_context_window_exceeded` an error or a stop reason?** It is an Anthropic `stop_reason` (SDK enum), not an HTTP error. agent-loop.md §5.3 lists it in the error table. Proposed: the adapter maps it to `StopReason=context_exceeded`, and the loop treats it exactly like the Window class (Tier 2, retry once). Fix the table wording.
- **Q11. Extend `Delta` with kinds?** Proposed: yes, additively (`Kind`, `CallID`, `Name`; the zero value = text, so current callers are unchanged). No usage deltas. Thinking deltas feed the details toggle. event-log.md §5.11's `Delta` gets the same fields and stays under 8 KB by the existing split rule.
- **Q12. Chat Completions adapter at all?** Proposed: no for OpenAI itself (Responses only: reasoning passback, and Chat Completions lacks function calling on GPT-6 Astra). Revisit only for OpenAI-compatible third parties on the roadmap (GLM, Ollama), as a separate adapter.
- **Q13. Should Provider server tools (Anthropic web search/fetch, OpenAI web search) ever replace our Platform Service web search?** Proposed: no in the base version. Web search is a metered Platform Service with our own `SearchProvider` (ADR 0006). A Provider server tool would bill the user's key and bypass our metering. The `NativePart` escape hatch keeps the door open without modelling them.

---

## Sources

**Anthropic**
- https://platform.claude.com/docs/en/api/messages
- https://platform.claude.com/docs/en/api/errors
- https://platform.claude.com/docs/en/api/rate-limits
- https://platform.claude.com/docs/en/api/models/list
- https://platform.claude.com/docs/en/build-with-claude/streaming
- https://platform.claude.com/docs/en/build-with-claude/prompt-caching
- https://platform.claude.com/docs/en/build-with-claude/token-counting
- https://platform.claude.com/docs/en/build-with-claude/files
- https://platform.claude.com/docs/en/build-with-claude/structured-outputs
- https://platform.claude.com/docs/en/agents-and-tools/tool-use/parallel-tool-use
- https://platform.claude.com/docs/en/about-claude/pricing
- https://github.com/anthropics/anthropic-sdk-go (incl. `message.go` on `main`)

**OpenAI**
- https://developers.openai.com/api/docs/guides/migrate-to-responses
- https://developers.openai.com/api/docs/guides/function-calling
- https://developers.openai.com/api/docs/guides/reasoning
- https://developers.openai.com/api/docs/guides/structured-outputs
- https://developers.openai.com/api/docs/guides/streaming-responses
- https://developers.openai.com/api/docs/guides/text-generation
- https://developers.openai.com/api/docs/guides/error-codes
- https://developers.openai.com/api/docs/guides/rate-limits
- https://developers.openai.com/api/docs/api-reference/models/list
- https://developers.openai.com/api/reference/resources/files/methods/create
- https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage/methods/costs
- https://github.com/openai/openai-go (incl. `responses/response.go` on `main`)
- https://github.com/openai/codex/blob/main/codex-rs/protocol/src/models.rs
- https://github.com/openai/codex/blob/main/codex-rs/model-provider-info/src/lib.rs
- https://openai.github.io/openai-agents-python/running_agents/

**Google / Gemini**
- https://ai.google.dev/api/generate-content
- https://ai.google.dev/api/models
- https://ai.google.dev/gemini-api/docs/function-calling
- https://ai.google.dev/gemini-api/docs/structured-output
- https://ai.google.dev/gemini-api/docs/files
- https://ai.google.dev/gemini-api/docs/api-errors
- https://ai.google.dev/gemini-api/docs/troubleshooting
- https://ai.google.dev/gemini-api/docs/generate-content/thought-signatures
- https://ai.google.dev/gemini-api/docs/pricing
- https://github.com/googleapis/go-genai (incl. `types.go`, `models.go` on `main`)
- https://docs.cloud.google.com/vertex-ai/generative-ai/docs/deprecations/genai-vertexai-sdk
- https://github.com/google-gemini/gemini-cli/blob/main/packages/core/src/core/geminiChat.ts

**Prior-art frameworks**
- https://github.com/vercel/ai/blob/main/packages/provider/src/language-model/v3/language-model-v3-content.ts
- https://github.com/vercel/ai/blob/main/packages/provider/src/language-model/v3/language-model-v3-reasoning.ts
- https://github.com/vercel/ai/blob/main/packages/anthropic/src/convert-to-anthropic-prompt.ts
- https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/messages.py
- https://github.com/pydantic/pydantic-ai/blob/main/pydantic_ai_slim/pydantic_ai/models/anthropic.py
- https://pydantic.dev/docs/ai/api/pydantic-ai/messages/
- https://docs.langchain.com/oss/python/langchain/messages
- https://reference.langchain.com/python/langchain-core/messages/content

**Build vs adopt, testing**
- https://github.com/BerriAI/litellm ; https://github.com/BerriAI/litellm/blob/main/model_prices_and_context_window.json ; https://docs.litellm.ai/docs/proxy/sync_models_github
- https://openrouter.ai/docs/guides/overview/auth/byok
- https://github.com/cloudwego/eino
- https://github.com/tmc/langchaingo
- https://github.com/sashabaranov/go-openai
- https://github.com/dnaeon/go-vcr

**jelly-fish (ground truth, not redesigned)**
- [../../CONTEXT.md](../../CONTEXT.md) ; [../product.md](../product.md) ; [../adr/0002-neutral-message-format.md](../adr/0002-neutral-message-format.md) ; [../adr/0006-byok-llm-platform-metered-services.md](../adr/0006-byok-llm-platform-metered-services.md) ; [../design/agent-loop.md](../design/agent-loop.md) ; [../design/context.md](../design/context.md) ; [../design/event-log.md](../design/event-log.md) ; [agent-loop.md](agent-loop.md)
