# Agents, Child Sessions, and Skills: Research

Status: research, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): an **Agent** is a saved configuration (instructions, model, tools, skills, permission mode, budget); a **Child Session** is a session with a `parent_id` started via `delegate(agent, task)`; a **Delegation** starts a Child Session; a **Skill** is a reusable pack of instructions and optional scripts; a **Slash Command** exposes agents and skills as shortcuts. Facts cite primary sources inline. **Unverified** marks anything not confirmed from a primary source. "⚠ cross-spec" flags conflicts or gaps against jelly-fish's own docs.

**Ground truth (already decided, not reopened here):** [event-log.md](../design/event-log.md): `session.created` carries the Agent config snapshot (incl. Budget), `parent_id`, `depth`; `child.started{child_session_id, agent, task, blocking}`; a blocking `delegate` parks the parent `awaiting_children` with no Worker (Decision 5); a non-blocking child's `child.completed` never wakes the parent (Decision 24); Interrupt always propagates to children (Decision 22); `session.completed` carries the child's result; export/delete cascade to children. [context.md](../design/context.md): children compact on their own log; child result capped at 2,000 tokens by default, configurable per Agent, overflow saved as an Artifact and linked (Decision 6); `delegate` results never cleared in Tier 1. [approver.md](../design/approver.md): child Permission Mode = stricter of parent's and its own Agent's (Decision 8); children get their own approval card labelled with the Agent name (Decision 14); `delegate` asks in ask mode, runs in auto/full-auto; Approval Rules are scoped to a Project or everywhere, never per Agent. [memory.md](../design/memory.md): Child Sessions can only `view` memory (Decision 4). [agent-loop.md](../design/agent-loop.md) Decision 15 / `CONTEXT.md` Budget: the wall-clock limit applies to Child Sessions.

---

## 1. Summary

- **Agent config fields in production**: instructions (system prompt), model (LLM choice), tools (capability allowlist), skills (preloaded expertise packs), permission mode (approval behavior), budget (token/cost/time limits). The consensus across Claude Agent SDK, OpenAI Agents, Gemini CLI, and others is that configs bundle these five to seven fields, with model and tools being optional (inherit defaults); instructions are always required. jelly-fish's own CONTEXT.md confirms the same set.
- **Child Session delegation**: three patterns exist: *context isolation* (child starts fresh unless forked, receives only a task prompt, returns only final text), *sync vs. async* (parent may wait for result synchronously or spawn asynchronously and check later), and *budget inheritance* (child gets a subset of parent's budget, never wider). Claude Agent SDK documents "only the final message returns to parent; intermediate tool calls stay inside the subagent," a design principle validated by independent implementations across Gemini CLI, OpenHands, and Codex CLI.
- **Skills**: Anthropic published Agent Skills as an open standard on 2025-12-18 ([agentskills.io](https://agentskills.io/specification); launch adopters per press included OpenAI, Microsoft, GitHub, Cursor; "40+ products" **unverified**). A skill is a folder with a required SKILL.md (name/description frontmatter + instructions) plus optional scripts and resources. Skills use *progressive disclosure*: discovery (load name+description only at startup), activation (load full SKILL.md when task matches), execution (run bundled code if needed). This keeps context footprint small while allowing many skills.
- **Skill security**: a real, documented problem in 2026. Snyk's ToxicSkills audit (2026-02-05) of 3,984 skills from ClawHub and skills.sh found at least one security flaw in 36.82%, a critical issue in 13.4%, and 76 confirmed malicious payloads. OpenClaw's ClawHub was hit by the ClawHavoc campaign (disclosed by Koi Security 2026-02-01; Antiy counted 1,184 malicious skills from 12 publisher accounts). Skill supply chain attacks follow the same industrialization path as traditional software supply chain threats.
- **UX for non-technical users**: no single "agent builder" UI found. Three patterns exist: *code-free configuration* (Claude Projects: system prompt + knowledge base + folder), *form-based creation* (Gemini Gems: name, instructions, default tool, upload files), and *retiring GPT builder* (ChatGPT custom GPTs retire 2026-12-11 in favor of plugins, where GPT instructions become a skill). jelly-fish should expect users to want form-based, not code-focused, UX for creating Agents.

---

## 2. Agent config fields in real products

### Claude Agent SDK (`AgentDefinition`)

Programmatic agent definition fields, verified from [code.claude.com/docs/en/agent-sdk/subagents](https://code.claude.com/docs/en/agent-sdk/subagents):

| Field | Type | Required | Purpose |
|---|---|---|---|
| `description` | string | Yes | Natural language cue for when to invoke this agent (e.g., "Expert code reviewer") |
| `prompt` | string | Yes | System prompt defining the agent's role and behavior |
| `tools` | string[] | No | Capability allowlist (e.g., `["Read", "Edit", "Bash"]`); omit to inherit all available tools |
| `model` | string | No | Model alias or full ID (e.g., `'sonnet'`, `'opus'`, `'inherit'`); omit for default |
| `skills` | string[] | No | Preloaded skill names; unlisted skills remain callable via Skill tool |
| `memory` | enum | No | Memory source: `'user'` / `'project'` / `'local'` |
| `mcpServers` | object[] | No | MCP servers by name or inline config |
| `permissionMode` | `PermissionMode` | No | Claude Code's own modes (not jelly-fish's ask/auto/full-auto); subagent inheritance rules decide when it applies |
| `disallowedTools` | string[] | No | Tools to remove (denylist; `mcp__server__*` patterns) |
| `omitClaudeMd` | boolean | No | Run as subagent without CLAUDE.md files |
| `maxTurns` | number | No | Max agentic turns before marking output as partial; when reached, agent can be resumed |
| `background` | boolean | No | Run this agent as non-blocking when invoked |
| `effort` | enum / number | No | Reasoning effort: `'low'` through `'max'`; maps to extended thinking |
| `initialPrompt` | string | No | Auto-submitted first turn (ignored when agent runs as subagent) |

**Subagent inheritance rules**: a non-fork child agent starts with a fresh context window but inherits only the task prompt passed by the parent, the project's CLAUDE.md (unless `omitClaudeMd: true`), and the list of other named agents it can send messages to.

### OpenAI Agents SDK (Handoff pattern)

OpenAI's handoff design ([openai.github.io/openai-agents-python/handoffs](https://openai.github.io/openai-agents-python/handoffs/)) treats agents as tools:

- **Handoff object**: wraps an Agent instance with metadata: target agent, `input_type` (customizable data schema), optional callbacks, optional tool name override.
- **LLM sees it as a tool**: named `transfer_to_<agent_name>`, callable when the model decides the task fits another agent's expertise.
- **Control transfer**: the receiving agent takes over the conversation, can see all prior history (though filtering is possible).
- **Configuration**: agents in OpenAI's SDK don't have a separate "agent definition" schema; instead, agents are Python class instances with methods for tool registration and instruction-setting.

**Key difference from Claude SDK**: OpenAI doesn't expose a declarative agent config format; agents are programmatic. jelly-fish's CONTEXT.md design (Agent as "a saved configuration") aligns more closely with Claude's declarative approach.

### Google Gemini CLI (Subagents)

Gemini CLI subagent delegation ([geminicli.com/docs/core/subagents](https://geminicli.com/docs/core/subagents/)):

- **Exposed as a tool**: "Subagents are exposed to the main agent as a tool of the same name. When the main agent calls the tool, it delegates the task to the subagent."
- **Delegation triggers**: automatic (main agent evaluates task fit) or forced (`@subagent_name` syntax).
- **Isolation**: each subagent maintains independent context, restricted toolsets, and recursion protection: "subagents **cannot** call other subagents", even with the `*` tool wildcard.
- **Config fields**: `name`, `description` (required); `kind` (local/remote), `tools` (wildcards), `mcpServers`, `model`, `temperature`, `max_turns`, `timeout_mins` (optional).

**Context isolation design**: nearly identical to Claude SDK's — subagent runs in "separate context loop, which saves tokens in your main conversation history" — only the final result returns.

### Consensus across products

| Field | Claude | OpenAI | Gemini | jelly-fish |
|---|---|---|---|---|
| Instructions / System prompt | Yes (required) | Yes (required) | Implicit (instructions in task) | Yes (required) |
| Model / Capability selection | Yes (optional) | Yes (programmatic) | Implicit | Yes (optional) |
| Tools / Capability allowlist | Yes (optional) | Yes (agent methods) | Yes (implicit) | Yes (optional) |
| Skills / Expertise packs | Yes (optional) | No (not yet) | No (not yet) | Yes (optional) |
| Permission mode / Approval behavior | Yes (optional) | Unverified | Unverified | Yes (optional) |
| Memory source | Yes (optional) | Unverified | Unverified | Yes (optional) |
| Budget / Cost/token limits | Yes (optional) | Unverified | Unverified | Yes (optional) |

---

## 3. Delegation and Child Sessions

### Context inheritance: three models

**Model 1 — Fresh start (default, most products)**
- Claude Agent SDK: "unless the subagent is a fork, its context window starts fresh... the only content you pass from parent to subagent is the Agent tool's prompt string."
- Gemini CLI: "Interactions with a subagent happen in a separate context loop."
- OpenAI handoffs: "The receiving agent essentially takes over the conversation and can access the entire previous interaction history" (optionally filtered).
- **Load-bearing design decision**: a fresh context isolates intermediate steps (tool calls, fetches, memory reads) inside the child, preventing token accumulation in the parent. jelly-fish already isolates the child's *log* (own session, own Compaction, capped result); ADR 0005 does not say what the child's *starting* context is (see below).

**Model 2 — Forked context (optional in Claude SDK)**
- When a child is a fork ([Claude Code: fork the current conversation](https://code.claude.com/docs/en/sub-agents#fork-the-current-conversation)), it inherits the parent's entire conversation context, tool results, and memory state.
- Used for parallel independent analyses that need the same background.

**Model 3 — Inheritance contract (Hermes Agent / OpenHands)**
- Task input includes: instructions, references, budget.
- Answer output includes: result, cost, findings.
- Per-agent runtime state lives on the child's own record, never shared with siblings.

**⚠ cross-spec with ADR 0005**: the ADR states "Child sessions get durability, budgets, approvals, and usage for free" but doesn't specify what context they inherit at startup. The parent passes a task prompt (clear), but does the child receive project instructions, the Project Files index, or the memory index (it can already `view` memory, memory.md Decision 4)? Claude Code's non-fork child gets its own prompt, the task, project CLAUDE.md (unless `omitClaudeMd`) and tool definitions; not the parent's history or system prompt. Claude SDK's model (no parent context unless fork) is documented and widely deployed; jelly-fish should adopt it explicitly in the agent-loop/delegation design doc.

### Sync vs. async child execution

**Synchronous (foreground)**: parent waits for child result before continuing. Used when parent needs child's answer to decide next steps.

**Asynchronous (background)**: parent spawns child without waiting. Used when parent can proceed independently. Claude Agent SDK: "Subagents run in the background by default... Claude sets `run_in_background: false` when it needs the result before continuing."

**Polling and resumption**: when a child completes while parent is parked (e.g., awaiting user approval), the parent is notified and can resume. Event-log.md §5.10 already specifies `awaiting_children` state, so jelly-fish's durability model handles both cases correctly.

### Return value: final message vs. transcript

**Parent receives**: only the child's final text message, never its intermediate tool calls or results.

Claude Agent SDK docs: "only its final message returns to the parent. A `research-assistant` subagent can explore dozens of files without any of that content accumulating in the main conversation. The parent receives a concise summary, not every file the subagent read."

**Full transcript persistence**: Claude Code stores each subagent's full JSONL transcript separately (not in parent's event log), accessible for audit but not in the parent's own context window.

**Best practice**, confirmed across multiple implementations: instruct each child to "return a summary, not a transcript" — the child's system prompt should say "report only the failing tests with their error messages, not the test run" to ensure parent sees findings, not verbose output.

**Partly decided**: context.md Decision 6 already caps the result (2,000 tokens default, per-Agent, overflow → linked Artifact) and `child.completed` carries `summary`. **⚠ Gap for grilling (§7, Q2)**: how the cap is enforced (child instructed vs. truncated vs. extra summarize call), and how the parent sees several results that arrive together.

### Budget inheritance and depth caps

**Budget narrowing**: child's budget is always narrower than or equal to parent's.

Claude Agent SDK: `CLAUDE_CODE_MAX_SUBAGENT_SPAWN_DEPTH` (env var, default 3) limits nesting depth, `CLAUDE_CODE_MAX_CONCURRENT_SUBAGENTS` (default 20) limits parallel subagents, `maxBudgetUsd` in options caps total spend (default: no limit) ([Agent SDK: cap subagent depth, concurrency, and spend](https://code.claude.com/docs/en/agent-sdk/subagents#cap-subagent-depth-concurrency-and-spend)). Claude Code subagents can spawn their own subagents; Gemini CLI forbids it.

**When a limit is hit**: Depth limit → child unable to spawn its own children (must do its own work); Concurrency limit → refuses new spawn ("Concurrent subagent limit reached") until the running count drops; Spend cap → refuses new spawn ("Budget limit reached"), stops running background subagents, ends query with `error_max_budget_usd` subtype.

**Cost tracking**: each subagent's API calls count toward the query's total spend. No surveyed product publishes a "child budget can't exceed parent's" invariant explicitly, but it's implicit in all implementations.

**jelly-fish's design**: ADR 0005 mentions "depth cap" but doesn't specify a default. Budget itself is already defined (`CONTEXT.md`, event-log.md §5.8, agent-loop.md Decision 15); each child gets its own from its Agent's snapshot. Open: whether a child's spend also counts against the parent's Budget. Recommend depth=3, concurrency=10 as starting defaults, matching Claude's scale.

---

## 4. Skills specification and progressive disclosure

### Anthropic Agent Skills open standard

Released 2025-12-18 ([agentskills.io](https://agentskills.io/specification); "40+ products" **unverified**). A skill is a folder containing:

```
my-skill/
├── SKILL.md          # Required: metadata + instructions
├── scripts/          # Optional: executable code (Python, bash, etc.)
├── references/       # Optional: documentation, examples
├── assets/           # Optional: templates, resources
└── ...
```

### SKILL.md format

**Frontmatter** (YAML):
```yaml
name: code-reviewer
description: Expert code review specialist. Use for quality, security, and maintainability reviews.
```

**Body**: plain markdown instructions telling the agent how to perform the skill's task. No fixed schema; spec recommends < 5,000 tokens and < 500 lines, with detail moved to referenced files.

**Frontmatter fields** ([spec](https://agentskills.io/specification)):
- `name` (required): ≤ 64 chars, lowercase letters/digits/hyphens, must match the folder name.
- `description` (required): ≤ 1,024 chars; what it does and when to use it.
- `license`, `compatibility` (≤ 500 chars, environment needs), `metadata` (string→string map) (optional).
- `allowed-tools` (optional, experimental): space-separated pre-approved tools, e.g. `Bash(git:*) Read`.
- There is no `tags`, `requires_approval`, or `scripts` field; `scripts/` is just a folder convention.

### Progressive disclosure lifecycle

1. **Discovery (session start)**: agent loads only `name` and `description` of all available skills (~100 tokens per skill, per the spec).
2. **Activation (on match)**: when task matches skill description, agent reads full SKILL.md into context (< 5,000 tokens recommended; cost only when the skill is relevant).
3. **Execution (on need)**: agent calls bundled scripts or loads reference files as required by instructions.

**Design principle**: agents can keep 50+ skills available with only a small discovery footprint. Contrast with a monolithic system prompt (token cost regardless of relevance).

### Script execution context

**Where scripts run**: product-dependent. Claude Code skills can execute scripts (Python, bash) inside the user's repo context (local); Gemini CLI runs scripts in its own sandbox; managed agents (e.g., Anthropic's hosted Managed Agents) run scripts in a server-side container.

**⚠ cross-spec with ADR 0003**: jelly-fish's "nothing executes on the host" rule means skill scripts must run only in the Sandbox, never on the jelly-fish server itself. This is consistent with the principle but hasn't been explicitly stated in a skills design doc yet — it should be, to avoid confusion (§6, Q4).

### Discovery and loading in jelly-fish's model

**Where skills live**: per-Project (project-scoped) or global (User-scoped). jelly-fish's CONTEXT.md lists Skill under project capabilities, so they're project-scoped; a Project may inherit global skills from the user's account.

**When agents see them**: Agent config has optional `skills: ["reviewer", "editor"]` field (from Claude SDK); unlisted skills remain accessible via Skill slash command but not preloaded. This matches Claude Code's two-tier model: preload only if explicitly listed, keep others available on-demand.

**Approval**: the spec has no approval flag. Its only permission hook is the experimental `allowed-tools` (pre-approval, i.e. the opposite direction). ⚠ cross-spec: honoring `allowed-tools` would create implicit Approval Rules, which ADR 0004 forbids; loading a Skill's instructions is a read, and any script it runs is an ordinary shell call in the Sandbox, approved as such.

---

## 5. Security: skill supply chain and prompt injection

### Documented malicious skill campaigns

**ClawHavoc (late Jan – Feb 2026)**: disclosed by Koi Security on 2026-02-01; Antiy Labs counted 1,184 malicious skills on OpenClaw's ClawHub from 12 publisher accounts (one uploaded 677). Skills posed as crypto bots and productivity tools; "ClickFix"-style instructions in long docs told users to run commands, and 335 installed the AMOS macOS stealer via fake prerequisites. (The Trivy/GitHub Actions compromise of 2026-03-19 is a real but unrelated CI supply-chain attack, not a skills attack; removed.)

Source: [Antiy Labs: ClawHavoc analysis](https://www.antiy.net/p/clawhavoc-analysis-of-large-scale-poisoning-campaign-targeting-the-openclaw-skill-market-for-ai-agents/)

**ToxicSkills audit (Snyk, 2026-02-05)**: scanned 3,984 skills from ClawHub and skills.sh:
- 36.82% (1,467) have at least one security flaw; 13.4% (534) at least one critical issue.
- Prompt injection in 2.6% of ClawHub skills; 76 confirmed malicious payloads.
- New-skill submissions went from under 50/day in mid-January to over 500/day by early February 2026.

Source: [snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub](https://snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub/)

### Attack vectors specific to skills

1. **Prompt injection via skill instructions**: a malicious skill's SKILL.md includes hidden prompts (e.g., white-on-white text, encoded commands) that override the agent's own instructions when activated.
2. **Exfiltration via side effects**: a skill's instructions tell the agent to call a Connector or tool with sensitive data embedded as arguments.
3. **Supply chain: marketplace trust**: users trust a skill because it's in an "official" marketplace, but the marketplace itself isn't notarized or has weak review processes.
4. **Rug-pull: silent updates**: a skill is updated maliciously after the user installed a benign version. No mechanism in production (as of research date) pins SKILL.md hashes to prevent this.

### Mitigations in production

**Snyk recommendation**: pin or hash skills at install time; audit marketplace-sourced skills with the same rigor as open-source dependencies.

**Arize AI recommendation**: isolate skills in a separate execution context (e.g., Sandbox) with limited tool access ([arize.com/blog/context-management-in-agent-harnesses](https://arize.com/blog/context-management-in-agent-harnesses/)).

**jelly-fish's built-in defenses**:
- Approver gates the tool calls a Skill's instructions lead to.
- Skill scripts can run in the Sandbox only (ADR 0003), never on the server.
- Skills don't silently gain tool access outside explicit `tools` config.

**⚠ Unverified**: whether jelly-fish should pin skill versions (e.g., via git commit hash or content-addressable storage) or implement a skill allowlist. The research found industry best practice (hash/pin) but no definitive jelly-fish requirement yet.

---

## 6. UX for non-technical users: agent builders

### Pattern 1 — Form-based (Gemini Gems)

**Interface**: multi-field form with validation.
**Fields**: Name, Instructions (with subfields: Persona, Task, Context, Format), Knowledge Files upload, Preview.
**Metadata**: no model selection; Gemini picks the model.
**Users who use it**: non-technical, prefer UI over code/YAML.

Source: [support.google.com/gemini/answer/15235603](https://support.google.com/gemini/answer/15235603)

**Pros**:
- Low friction for non-code-fluent users.
- Form validation prevents syntax errors.
- Inline preview before save.

**Cons**:
- Hard to version-control; lives in Google's database.
- No team workflow (share via link, no branching).
- Limited to Google's field schema.

### Pattern 2 — Code-free project config (Claude Projects)

**Interface**: markdown + file upload UI.
**What goes in**: system instructions (typed in text field or uploaded as `.md`), knowledge base (files uploaded), chat list (auto-populated).
**Metadata**: no explicit "choose tools/model" in the UI; inherits from Claude's defaults.
**Users**: researchers, product teams that think in prose and documents.

Source: [claude.com/blog/projects-redesigned](https://claude.com/blog/projects-redesigned)

**Pros**:
- CLAUDE.md is version-controlled (team can edit, review, commit).
- Rich formatting (markdown supports links, code blocks).
- Knowledge base is discoverable from a Files tab.

**Cons**:
- Requires familiarity with markdown editing.
- No explicit Skill/Agent selection UI (limited to system instructions).
- jelly-fish's own CONTEXT.md is project-scoped but not yet surfaced in UX.

### Pattern 3 — Retiring: ChatGPT custom GPTs

**Status**: OpenAI is retiring custom GPTs in favor of plugins. Creating new GPTs ends 2026-10-26 (already unavailable on personal plans); GPTs stop working 2026-12-11 (Enterprise deferral to 2027-02-11). On migration, a GPT's instructions become a skill and connected apps carry over; custom actions and the selected model don't. Dates from the Help Center FAQ via search snippet (page returned 403 to direct fetch).

Source: [OpenAI Help Center: Custom GPT retirement and migration FAQ](https://help.openai.com/en/articles/20001519-custom-gpt-retirement-and-migration-faq)

**What it did**: form-based builder (Name, Description, Instructions, Files upload, Actions/integrations selection).

**Lesson**: the market is converging on "instructions = a Skill", bundled with connectors; a stand-alone persona builder is being folded into that.

### Recommendation for jelly-fish

jelly-fish users include "non-technical friends," per the project brief. A Slack-like or Figma-like app needs:

1. **Form-based Agent creation** (similar to Gemini Gems): Name, Description, System Instructions (textarea), Model selector, Tools selector (checkboxes), Skills selector (searchable list).
2. **Project file upload**: add knowledge files to a Project (separate from Agent config).
3. **Preview/testing** before save: run a test turn in the Agent with a sample prompt, show result.
4. **No code editing required**: users never see YAML or frontmatter; the form writes the stored Agent/Skill directly.

**Skill selection in the form**: list available skills (discoverable by name/description), allow user to check which ones the Agent should preload. This matches Claude SDK's `AgentDefinition.skills` field.

---

## 7. Open questions for grilling

1. **How should jelly-fish's Agent config fields map to the standard?** jelly-fish's CONTEXT.md defines Agent as "instructions, model, allowed tools, skills, permission mode, budget" — does this cover all five + budget, or are there other fields (memory source, MCP servers, effort level) that matter for the base version? Proposed: commit to the CONTEXT.md set (connectors already cover MCP servers; memory access is already fixed per session type by memory.md), plus the per-Agent child result cap from context.md Decision 6. Touches: CONTEXT.md, agent-loop.md.

2. **When a Child Session completes, what exactly does the parent receive?** Full final message text from the child's last turn, or an extracted/summarized result field? If children run in parallel, does parent see them concatenated, or does the Approver batch them? Proposed: parent receives only the child's final text message (matching Claude SDK behavior), batched at the parent's next turn by the Fold/Decide engine. A child's tool calls and intermediate results never appear in parent's event log, only in the child's own durable transcript (for audit). Touches: event-log.md, agent-loop.md.

3. **What is the default context inheritance for Child Sessions?** A fresh context (child knows only the task prompt) is the default in three surveyed products; should jelly-fish adopt this, or allow parents to fork (child inherits parent context) as an option? Proposed: fresh start by default (ADR 0005 is silent; decide explicitly), and optionally a fork parameter to the delegate tool for cases where child needs parent's full context. Touches: ADR 0005, agent-loop.md.

4. **Where do Skill scripts execute in jelly-fish?** ADR 0003 says "nothing executes on the host"; Skill scripts run on many platforms (local in Claude Code, server-side in managed agents). Does jelly-fish require skills to bundle scripts that run *only* in the Sandbox? Proposed: yes, for the base version — any script bundled in a Skill must be explicitly approved and executed inside the Sandbox, never on the jelly-fish server. This is consistent with ADR 0003 and matches managed-agent best practice. Touches: ADR 0003, agent-loop.md (once Sandbox is designed).

5. **Should jelly-fish implement Skill version pinning or hashing?** ToxicSkills research documents the risk of marketplace-sourced skills being silently updated. Proposed: require Project-scoped skills (stored on the server) to be pinned to a git commit hash or content-addressable blob ID; global skills from a marketplace are warned as "may be updated without notice" but can still be used. This mirrors npm audit/lock practices. Touches: new design doc or a skills spec.

6. **Is the `delegate` task text trusted input for Jev in the child?** Already decided: the child runs its own Approver chain, its mode is the stricter of parent's and its Agent's (approver.md Decision 8), and it has its own card (Decision 14). Remaining: the task text is written by the parent *model*, which may have read injected content, so should Jev treat it as the child's "user intent" or as untrusted? Touches: approver.md.

7. **Should non-technical users be able to create Agents and Skills, or only Admins?** Form-based UX (Gems pattern) assumes any user can create; code-based (YAML/git) assumes admin/power-user. Proposed: both — provide a form-based builder for non-technical users (create project-scoped Agents), and allow power users to commit YAML to the Project's repo for version control. The form serializes to YAML backend transparently. Touches: new UI/product doc, not this research.

8. **Does an Agent's tool allowlist interact with Approval Rules?** Already decided: rules are scoped to a Project or everywhere, never per Agent, and there are no deny rules (approver.md, ADR 0004). Remaining: a tool not in the Agent's allowlist is simply absent from the child (like Claude Code), so a rule for it never fires; confirm that, and that rules apply to children the same as to their parent. Touches: approver.md.

9. ~~Child Sessions and Quota~~ Merged: Quota is per User, so children count automatically. The real question is Budget: does a child's spend also count against the parent's Budget (see §3)? Folded into Q2/Q3 grilling.

10. ~~`requires_approval` enforcement~~ Dropped: the field does not exist in the Agent Skills spec. Replaced by: **what to do with a Skill's `allowed-tools`?** Proposed: ignore it (honoring it would create implicit Approval Rules, ADR 0004). Touches: approver.md.

---

## Sources

**Claude Agent SDK** (fetched directly, current as of 2026-09-27)
- https://code.claude.com/docs/en/agent-sdk/subagents
- https://hidekazu-konishi.com/entry/claude_code_subagents_and_orchestration_guide.html

**OpenAI Agents SDK**
- https://openai.github.io/openai-agents-python/handoffs/
- https://futureagi.com/blog/evaluating-openai-agents-sdk-2026/

**Google Gemini CLI**
- https://geminicli.com/docs/core/subagents/
- https://www.infoq.com/news/2026/04/subagents-gemini-cli/

**Agent Skills standard**
- https://agentskills.io/home
- https://github.com/agentskills/agentskills
- https://thenewstack.io/agent-skills-anthropics-next-bid-to-define-ai-standards/

**Skills security research**
- https://snyk.io/blog/toxicskills-malicious-ai-agent-skills-clawhub/
- https://www.antiy.net/p/clawhavoc-analysis-of-large-scale-poisoning-campaign-targeting-the-openclaw-skill-market-for-ai-agents/
- https://agentskills.io/specification (frontmatter fields, verified 2026-09-27)
- https://help.openai.com/en/articles/20001519-custom-gpt-retirement-and-migration-faq
- https://www.helpnetsecurity.com/2026/06/11/owasp-prompt-injection-ai-security-failures/

**Agent builder UX**
- https://support.google.com/gemini/answer/15235603 (Gemini Gems)
- https://claude.com/blog/projects-redesigned (Claude Projects)
- https://www.ai-toolbox.co/chatgpt-management-and-productivity/how-to-use-claude-projects-guide-2026
- https://www.ai-toolbox.co/chatgpt-management-and-productivity/how-to-create-custom-gpts-walkthrough-2026

**Context inheritance and Child Session behavior**
- https://github.com/anthropics/claude-code/issues/25754
- https://arize.com/blog/context-management-in-agent-harnesses/

**jelly-fish (ground truth, not redesigned)**
- [../../CONTEXT.md](../../CONTEXT.md) ; [../adr/0003-nothing-executes-on-the-host.md](../adr/0003-nothing-executes-on-the-host.md) ; [../adr/0004-layered-approver-no-implicit-rules.md](../adr/0004-layered-approver-no-implicit-rules.md) ; [../adr/0005-agents-and-child-sessions-unified.md](../adr/0005-agents-and-child-sessions-unified.md) ; [../adr/0006-byok-llm-platform-metered-services.md](../adr/0006-byok-llm-platform-metered-services.md) ; [../design/agent-loop.md](../design/agent-loop.md) ; [../design/event-log.md](../design/event-log.md) ; [../design/approver.md](../design/approver.md) ; [../../TODO.md](../../TODO.md)
