# Memory: Research

> Background only. The build spec is [../design/memory.md](../design/memory.md); implementers read that, not this.

Status: research, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Every factual claim has an inline primary source. Anything I could not check is marked **unverified**.

Scope: **cross-session memory** only (Project Memory and User Memory). In-session context management research is in [context.md](context.md).

---

## 1. Problem in our runtime

What the ADRs and product scope force on us:

| Constraint | Source | What it means for memory |
|---|---|---|
| The **Event Log** is the only source of truth for a Session | [ADR 0001](../adr/0001-own-durable-execution-on-postgres.md), CONTEXT.md | Memory writes are recorded as events; the `memories` table is a projection of current state. |
| Neutral message format; model switch mid-session | [ADR 0002](../adr/0002-neutral-message-format.md) | The memory tool must work the same on every Provider. No vendor-native memory types. |
| BYOK | [ADR 0006](../adr/0006-byok-llm-platform-metered-services.md) | Any LLM pass over memory (Tidy memory) spends the *user's* key, so it is Usage and counts against the Budget. |
| Multi-tenant: Workspace → User → Project → Session | CONTEXT.md | Every memory row is scoped to a tenant. Project Memory is per project, as CONTEXT.md defines it. |
| **Child Sessions** are normal sessions ([ADR 0005](../adr/0005-agents-and-child-sessions-unified.md)) | ADR 0005 | What a Child Session may read or write must be decided ([spec](../design/memory.md) Decision 4). |
| Hard delete + JSON export | [product.md](../product.md) "Data" | Memory must be fully removable (including revisions) and included in exports. |
| Non-technical users | product.md | Memory must be visible, editable and explainable in the UI. No hidden "it just knows" behavior. |
| Skills load on demand | CONTEXT.md | A Skill is already *procedural* memory. Don't build a second mechanism for it. |
| Semantic memory (pgvector) is on the later roadmap | product.md | The base version must work without embeddings, with a schema that can take a vector column later. |

---

## 2. Taxonomy

| Layer | Scope / lifetime | Examples | jelly-fish term |
|---|---|---|---|
| In-session context management | One Session | Compaction, tool-result clearing | **Compaction**, see [context.md](context.md) |
| Cross-session, project-scoped | Project, until deleted | Decisions, conventions, "the client prefers X" | **Project Memory** |
| Cross-project, user-scoped | User, until deleted | "I'm a nurse, write simply", timezone | **User Memory** |
| Transcript recall | Past Sessions in the Project | Search old chats | Event Log search, deferred ([spec](../design/memory.md) Decision 10) |

Cognitive-science split, used by [LangMem](https://langchain-ai.github.io/langmem/concepts/conceptual_guide/) and [LangGraph](https://docs.langchain.com/oss/python/langgraph/memory):
- **Semantic** = facts. Either a *profile* (one schema-bound document that gets updated) or a *collection* (many records that need reconciling) ([LangMem](https://langchain-ai.github.io/langmem/concepts/conceptual_guide/)).
- **Episodic** = past experiences and examples, often used as few-shot examples ([LangGraph](https://docs.langchain.com/oss/python/langgraph/memory)).
- **Procedural** = rules and instructions, including prompts refined through reflection ([LangGraph](https://docs.langchain.com/oss/python/langgraph/memory)). For us, this is **Skills**, Agent instructions and Project instructions.
- **Semantic/vector retrieval** is a *read strategy*, not a memory type. It ranks stored items by embedding similarity, often fused with BM25 and other signals ([Graphiti](https://github.com/getzep/graphiti), [Mem0 README](https://github.com/mem0ai/mem0)).

---

## 3. What exists now

### Anthropic
- **Memory tool** (`memory_20250818`). It is client-side: Claude asks for `view/create/str_replace/insert/delete/rename` under `/memories`, and your app does the storage. It works on all Claude 4+ models and needs no beta header. When present, the API injects "ALWAYS VIEW YOUR MEMORY DIRECTORY BEFORE DOING ANYTHING ELSE… ASSUME INTERRUPTION". The docs list the security work as yours: path-traversal checks, size caps, "periodically delete memory files that haven't been accessed in a long time", and stripping sensitive data ([docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)).
- **Claude Code** ([memory docs](https://code.claude.com/docs/en/memory)):
  - `CLAUDE.md` is a human-written hierarchy (user → project → subdirectory) with a target of "under 200 lines".
  - **Auto memory** writes to `~/.claude/projects/<project>/memory/`. It has a `MEMORY.md` index; only its first 200 lines or 25 KB load at start. Topic files are read on demand.
  - Notes are typed `user | feedback | project | reference`. Claude "skips anything it can derive from the codebase".
  - Claude Code nags Claude to merge or drop stale entries when the index nears its limit. Memory files are exempt from the transcript retention sweep.
  - Subagents don't inherit the main memory, but can have their own `memory: user|project|local` directory ([subagents](https://code.claude.com/docs/en/sub-agents)).
- **Claude apps**:
  - Memory is *project-scoped*: "Each project has its own separate memory space" ([help](https://support.claude.com/en/articles/11817273-use-claude-s-chat-search-and-memory-to-build-on-previous-context)).
  - The newer system saves "individual topics as you chat" instead of a daily summary.
  - Incognito chats are never saved.
  - Deleting a chat does **not** remove the memories derived from it.
  - Launched Sep 11 2025 (Team/Enterprise) and Oct 23 2025 (Pro/Max) ([blog](https://claude.com/blog/memory)).

### OpenAI
- **ChatGPT**: two layers, "saved memories" (an explicit list the user can edit) and "reference chat history" (implicit, changes over time).
  - Turning chat history off schedules derived info "for deletion … within 30 days".
  - Deleting a chat doesn't delete its saved memory.
  - Temporary Chats neither use nor create memories.
  - Source: help-center text as returned by search for [Memory FAQ](https://help.openai.com/en/articles/8590148-memory-faq) and [announcement](https://openai.com/index/memory-and-new-controls-for-chatgpt/). The pages themselves returned HTTP 403 to my fetches, so the wording is **not fully verified**.
  - Third-party sites claim a June 2026 redesign ("Dreaming" background process): **unverified**, no first-party source.

### Google Gemini
- **Gemini app**: "Personal context → Memory" (formerly "past chats"). On by default for adults with personal accounts, and needs Keep Activity. Temporary Chats are excluded ([help](https://support.google.com/gemini/answer/16598469), [blog](https://blog.google/products-and-platforms/products/gemini/temporary-chats-privacy-controls/)).
- **Gemini API**: no memory endpoint that I could find (API-level state and caching are in [context.md](context.md)).
- **ADK / Vertex Memory Bank**:
  - `MemoryService` offers `add_session_to_memory` and `search_memory`, plus `load_memory` (the agent decides when) and `preload_memory` (automatic at start) tools. `VertexAiMemoryBankService` "intelligently consolidates" new memories with existing ones ([ADK](https://adk.dev/sessions/memory/)).
  - Memory Bank has profiles, revisions and IAM ([overview](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/agent-engine/memory-bank/overview)). I could not verify details of its TTL or contradiction handling.

### Letta / MemGPT
- Letta has three tiers ([docs](https://docs.letta.com/guides/agents/memory)):
  - **Core memory blocks**: label, value and character limit; can be read-only or shared; "pinned to the system prompt".
  - **Archival memory**: long-term store.
  - **Recall memory**: searchable message history.
- Tools include `memory_replace`, `memory_insert`, `memory_rethink`, `archival_memory_insert/search` and `conversation_search`.
- **Sleep-time agents** are background subagents that "review recent conversations, consolidate useful lessons, and update memory". They run after N steps or on compaction, with an optional review step ([docs](https://docs.letta.com/guides/agents/architectures/sleeptime); the page seems to have moved, so the final URL is uncertain).
- The paper reports about 5x less test-time compute for the same accuracy ([arXiv 2504.13171](https://arxiv.org/abs/2504.13171)).
- MemGPT's virtual context management is in [context.md](context.md).

### LangGraph / LangMem
- Short-term memory is thread-scoped (checkpointer). Long-term memory is a namespaced `Store` shared across threads ([docs](https://docs.langchain.com/oss/python/langgraph/memory)).
- **Hot path** (write during the turn: adds latency, available immediately) vs **background** (write after the turn: no added latency, but needs a trigger policy) ([LangMem](https://langchain-ai.github.io/langmem/concepts/conceptual_guide/)).
- LangMem ships a `create_memory_manager` for extract/update/consolidate, `create_manage_memory_tool` for agent-driven writes, and `create_prompt_optimizer` for procedural memory.
- TTL: store items take `default_ttl` and `refresh_on_read` (default true); checkpoints use `strategy: delete | keep_latest` ([TTL docs](https://docs.langchain.com/langsmith/configure-ttl)).

### Mem0
- The **2025 paper** has an extraction phase and an update phase. The update phase picks one of **ADD / UPDATE / DELETE / NOOP** against similar existing memories. Mem0g adds a graph. Reported results: +26% LLM-judge vs OpenAI memory, 91% lower p95 latency, >90% fewer tokens on LOCOMO ([arXiv 2504.19413](https://arxiv.org/abs/2504.19413)).
- **Current OSS (v3) changed this**: "Single-pass ADD-only extraction: one LLM call, no UPDATE/DELETE. Memories accumulate; nothing is overwritten." Retrieval is multi-signal (semantic + BM25 + entity) and time-aware ([README](https://github.com/mem0ai/mem0)). Docs describe it as an "additive pipeline". Scoping uses `user_id/agent_id/app_id/run_id`. An optional `expiration_date` hides memories from search ([docs](https://docs.mem0.ai/core-concepts/memory-operations/add)).
- **Takeaway**: the market leader moved contradiction handling from write time to read time.

### Zep / Graphiti
- A temporal knowledge graph with a **bi-temporal** model (event time vs ingestion time) and three layers: episode, semantic-entity and community subgraphs. When facts contradict, the old edges are *invalidated*. Reported 94.8% vs 93.4% (MemGPT) on DMR, and up to +18.5% accuracy with 90% lower latency on LongMemEval ([arXiv 2501.13956](https://arxiv.org/abs/2501.13956)).
- Each fact carries `valid_at`/`invalid_at`. "Old facts are invalidated, not deleted." Retrieval is hybrid (embeddings + BM25 + graph traversal). Backends: Neo4j, FalkorDB, Neptune (Kuzu deprecated) ([repo](https://github.com/getzep/graphiti)).

### Cognee
- An OSS memory platform that turns documents, code and conversations into a self-hosted knowledge graph ([repo](https://github.com/topoteretes/cognee)). Relevant only if we later want graph memory over Uploads. Skipped otherwise.

### Research reference: Generative Agents
- A memory stream plus retrieval, where score = α·recency + α·importance + α·relevance, each min-max normalized and all α = 1. Recency is exponential decay with factor **0.995 per game-hour since last retrieval**. Importance is an LLM-rated **1-10 "poignancy"**. Relevance is embedding cosine. **Reflection** runs when the summed importance of recent events passes **150** ([paper](https://arxiv.org/abs/2304.03442)).

### Comparison

| System | Scope | Write path | Read path | Update / contradiction | Expiry / decay | User control | Storage |
|---|---|---|---|---|---|---|---|
| Anthropic memory tool | App-defined | Agent tool call | Agent browses files | Agent edits files | App's job ("delete unaccessed") | App's job | Yours (client-side) |
| Claude Code auto memory | Repo (+ subagent) | Agent, in session | Index always loaded (200 lines / 25 KB) + files on demand | Agent merges when nudged | None automatic | Plain files, `/memory` | Local disk |
| Claude apps | Per project | Agent, in session ("topics") | Automatic + chat search | Not documented | Not documented | View/edit/delete; incognito | Anthropic |
| ChatGPT | User | Saved: explicit; history: implicit | Automatic | History "can change" | 30-day delete when off (not fully verified) | View/delete; temporary chat | OpenAI |
| Vertex Memory Bank | User / app | Background from session | `preload_memory` / `load_memory` | "Consolidates" | Unverified | Revisions | Google |
| Letta | Agent (blocks can be shared) | Agent tools + sleep-time agent | Core pinned; archival/recall search | Agent rewrites blocks | None documented | API | Postgres/DB |
| LangMem | Namespace | Hot path or background | Search / get | Manager reconciles | Store TTL | App's job | LangGraph Store |
| Mem0 v3 | user/agent/run | Background extract (ADD-only) | Hybrid retrieval | At read time (temporal ranking) | `expiration_date` | API | Vector DB |
| Graphiti | Graph group | Ingest episodes | Hybrid + graph | Edge invalidation, bi-temporal | Validity intervals | API | Graph DB |

---

## 4. Memory lifecycle

| Stage | Options | Who does it |
|---|---|---|
| **Write: agent tool call (hot path)** | The agent decides to save | Anthropic memory tool, Claude Code, Letta, LangMem `manage_memory_tool` |
| **Write: background extraction** | An LLM pass over a finished or idle session | Mem0 `add`, Vertex Memory Bank, LangMem manager, Letta sleep-time agent, ChatGPT chat history |
| **Write: explicit user** | "Remember X", a settings UI, import | ChatGPT saved memories, Claude import ([help](https://support.claude.com/en/articles/12123587-import-and-export-your-memory-from-claude)), CLAUDE.md |
| **Read: always in prompt** | A small pinned index or blocks | Claude Code `MEMORY.md`, Letta core blocks, ADK `preload_memory` |
| **Read: retrieval** | Top-k by similarity or hybrid score | Mem0, Graphiti, Letta archival, Generative Agents |
| **Read: agent-driven browsing** | The agent calls view/search on demand | Anthropic memory tool, Claude Code topic files, ADK `load_memory`, Letta `conversation_search` |
| **Update / dedupe** | Write-time reconcile (ADD/UPDATE/DELETE/NOOP), or add-only with read-time resolution | Mem0 paper (write-time) → Mem0 v3 (read-time) |
| **Contradiction** | Overwrite, invalidate with validity interval, or keep both and rank by time | Graphiti (invalidate), Mem0 v3 (temporal ranking), file-based systems (agent overwrites) |
| **Decay / expiry** | TTL, refresh-on-read, recency × importance scoring | LangGraph TTL, Mem0 `expiration_date`, Generative Agents scoring, Anthropic "delete unaccessed" advice |
| **Consolidation** | Periodic merge, summarize or reflect | Letta sleep-time, Generative Agents reflection (at importance ≥150), Claude Code index-size nudge |
| **Temporal validity** | `valid_at` / `invalid_at` | Graphiti |
| **User review / edit / delete** | UI list, per-item delete, incognito | ChatGPT, Claude, Gemini, Claude Code (plain files) |

Consistent finding: **deleting a chat does not delete memories derived from it** in ChatGPT and Claude (sources above). Memory has its own lifecycle and needs its own delete UI.

---

## 5. Options for jelly-fish

### A. Agent-managed memory files in Postgres (Claude Code / Anthropic memory tool style)
- The agent reads and writes memory "files" through one `memory` tool. A short index is always in the prompt; entries are read on demand.
- **Pros**: simplest; no embeddings; transparent to users (each entry is readable text); well tested in Claude Code.
- **Cons**: quality depends on the model's discipline; no semantic search; the index grows until something consolidates it.

### B. A plus background consolidation and extraction (Letta sleep-time / Mem0 style)
- Same store as A. A background job reviews recent Sessions and proposes ADD/UPDATE/DELETE on memories.
- **Pros**: catches facts the agent missed; keeps the index tidy.
- **Cons**: spends the user's key while they're away (surprising under BYOK); harder to explain to users; a new place for poisoning to spread.

### C. Retrieval memory (pgvector + FTS, Mem0 v3 style, add-only)
- Many small memory items; top-k hybrid retrieval into the prompt each turn; conflicts resolved at read time by recency.
- **Pros**: scales to large memories; no reconcile step.
- **Cons**: needs an embedding Provider. BYOK keys may not include embeddings (Anthropic has no embedding API), so it becomes a metered Platform Service. Retrieved items change every turn, which **breaks prompt caching** ([context.md](context.md) §4). Harder for users to understand. It's already on the "later" roadmap.

### D. Temporal knowledge graph (Graphiti style)
- Entities and edges with validity intervals.
- **Pros**: best for "what was true when".
- **Cons**: heavy (graph DB or complex SQL); overkill for chat plus tasks for friends.

---

## 6. Cross-cutting choices

| Concern | Choice |
|---|---|
| Scoping | Two scopes: `user` and `project`. The agent picks the scope. Per-project toggle "Use my personal memory" (default on). No agent scope. |
| Provider neutrality | Our own `memory` function on every Provider, stored in our tables. No `memory_20250818`, no vendor server-side memory. |
| Privacy | Hard delete removes the row and all its revisions. Export includes memories. Every write is a visible chip. Incognito sessions neither read nor write memory. |
| Deletion of the source | Deleting a Session keeps memories derived from it (matches ChatGPT and Claude); `source_session_id` becomes NULL. The UI shows each memory's source session while it exists. Deleting a Project deletes its Project Memory; User Memory is unaffected. |
| Security | See §7 and the spec. |

---

## 7. Threat: memory poisoning

The threat is real: MINJA poisons agent memory "via query-only interaction", without direct access to the memory store ([arXiv 2503.03704](https://arxiv.org/abs/2503.03704)). For us, the main vector is web fetch or Connector output that tells the agent to "remember" something, which then persists into future Sessions. Mitigations chosen are in the spec.

---

## 8. Conclusion

Build Option A, for both scopes. Hot-path writes only. Consolidation is B's idea behind a review gate: a user-triggered "Tidy memory" (the Letta sleep-time pattern with a review gate and no background spend, [docs](https://docs.letta.com/guides/agents/architectures/sleeptime)). Keep the schema ready for C (pgvector) later. Anthropic's native `memory_20250818` is not used: it has no scope/title/kind fields. Stale detection follows Anthropic's "delete files not accessed in a long time" advice ([docs](https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool)), with a human deciding; size-pressure consolidation is Claude Code's mechanism ([docs](https://code.claude.com/docs/en/memory)). Overwrite-plus-revisions gives cheap validity history without Graphiti's full bi-temporal model.

Full spec and decisions: [../design/memory.md](../design/memory.md).

---

## 9. Sources

Anthropic
- Memory tool: https://platform.claude.com/docs/en/agents-and-tools/tool-use/memory-tool
- Claude Code memory: https://code.claude.com/docs/en/memory
- Claude Code subagents: https://code.claude.com/docs/en/sub-agents
- Claude app memory help: https://support.claude.com/en/articles/11817273-use-claude-s-chat-search-and-memory-to-build-on-previous-context
- Claude memory import/export: https://support.claude.com/en/articles/12123587-import-and-export-your-memory-from-claude
- Claude memory launch: https://claude.com/blog/memory

OpenAI
- ChatGPT Memory FAQ (403 to fetch; content via search): https://help.openai.com/en/articles/8590148-memory-faq
- Memory announcement (403 to fetch): https://openai.com/index/memory-and-new-controls-for-chatgpt/

Google
- Gemini app memory help: https://support.google.com/gemini/answer/16598469
- Temporary chats / past chats blog: https://blog.google/products-and-platforms/products/gemini/temporary-chats-privacy-controls/
- ADK memory: https://adk.dev/sessions/memory/
- Vertex Memory Bank: https://docs.cloud.google.com/vertex-ai/generative-ai/docs/agent-engine/memory-bank/overview

Frameworks and papers
- Letta memory: https://docs.letta.com/guides/agents/memory
- Letta sleep-time agents: https://docs.letta.com/guides/agents/architectures/sleeptime
- Sleep-time compute: https://arxiv.org/abs/2504.13171
- LangGraph memory: https://docs.langchain.com/oss/python/langgraph/memory
- LangMem concepts: https://langchain-ai.github.io/langmem/concepts/conceptual_guide/
- LangGraph TTL: https://docs.langchain.com/langsmith/configure-ttl
- Mem0 paper: https://arxiv.org/abs/2504.19413
- Mem0 README (v3 ADD-only): https://github.com/mem0ai/mem0
- Mem0 add docs: https://docs.mem0.ai/core-concepts/memory-operations/add
- Zep paper: https://arxiv.org/abs/2501.13956
- Graphiti: https://github.com/getzep/graphiti
- Cognee: https://github.com/topoteretes/cognee
- Generative Agents: https://arxiv.org/abs/2304.03442
- MINJA memory injection: https://arxiv.org/abs/2503.03704
