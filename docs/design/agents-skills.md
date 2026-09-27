# Agents, Child Sessions and Skills: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): an **Agent** is a saved configuration (never bot, assistant, persona); the **Default Agent** is the one a project uses for new sessions ("General" unless changed); a **Child Session** is a session started by another through **Delegation** with the `delegate` tool (never subtask, spawning, handoff); a **Skill** is a reusable pack of instructions and optional scripts (never plugin, template, prompt); a **Slash Command** is a chat-input shortcut; the isolated container is the **Sandbox** (never computer). Consistent with ADR [0003](../adr/0003-nothing-executes-on-the-host.md) (nothing executes on the host), ADR [0004](../adr/0004-layered-approver-no-implicit-rules.md) (no implicit Approval Rules), ADR [0005](../adr/0005-agents-and-child-sessions-unified.md) (Agents and Child Sessions are one mechanism), [event-log.md](event-log.md) §5.9–§5.10 and Decisions 5, 22, 24 (Budget, delegation parking, Interrupt propagation, non-blocking children), [context.md](context.md) §5.2–§5.3 and Decisions 5–6 (prompt layout, Skill re-injection, child result cap), [approver.md](approver.md) Decisions 8, 14 (child mode, child card), [memory.md](memory.md) Decision 4 (children only `view` memory). Background and prior art: [../research/agents-skills.md](../research/agents-skills.md).

## 1. Summary

- Agents and Skills are saved in one Project or everywhere (all the user's Projects); a project one wins over an everywhere one with the same name (D1). Every user creates them with forms; Skills can also be imported as `.zip` / `SKILL.md` (D2).
- An Agent is: description, instructions, model (or "Same as session"), allowed tools and Connectors, Skills, Permission Mode, Budget, and child result cap (D3). A session snapshots its resolved Agent in `session.created`; edits reach only new sessions and `/agent` switches (D14).
- A Skill is name + description + instructions body (≤ 5k tokens) + optional files. The prompt only ever carries each available Skill's name and description; the body loads through `load_skill(name)` or `/skill-name` and then stays for the session (D9, D10, D16).
- An Agent's `skills` list is an allowlist of what the *model* sees; the user can load any visible Skill with `/skill-name` (D9, D19).
- Skill scripts run only in the Sandbox: the Skill's files are copied into it when one is attached; without one, the agent requests it through the normal Sandbox approval or follows the instructions alone (D4).
- `delegate(agent, task, blocking=true)` starts a Child Session with a fresh context; several calls in one turn run in parallel (D5, D6). Depth ≤ 2, ≤ 5 running children per session (D7, D8).
- A child's tokens and dollars also count against its parent's Budget when it completes (D11). Results over the cap become an Artifact plus the first ~2,000 tokens (D12).
- Children share the parent's Sandbox (D20). The user can Stop one child from its card but can't steer it (D23).
- `/agent`, `/model` and `/mode` are recorded as one event, `session.config_changed` (D17, D18).

## 2. Scope

**In the base version**
- `agents` and `skills` tables with project / everywhere scope; name resolution (§5.1).
- Agent form, Skill form, Skill import (`.zip` / `SKILL.md`).
- The `delegate` and `load_skill` built-in tools.
- Child Session start context, depth and concurrency caps, Budget roll-up, result cap enforcement.
- Slash Commands `/agent <name>` and `/<skill-name> [text]`.
- New event `session.config_changed`.
- Default Agent "General" and its defaults.
- Child cards with Stop.

**Out (deferred)**
- Forking the parent's context into a child (D5).
- Always-on Skills; nothing is preloaded (D16).
- A Skill marketplace and remote Skill updates (D13, D24).
- Honoring a Skill's `allowed-tools` (D15).
- Steering a Child Session directly (D23).
- Sharing Agents or Skills with other users (product.md: teams/workspaces later).
- How Skill files are laid out and copied into the Sandbox, and how a shared Sandbox is owned across a session tree: Sandbox spec ([#23](https://github.com/bhanuprakaash/jelly-fish/issues/23)).
- A test run inside the Agent form before saving; start a chat with the new Agent instead (D30).
- Tuning the caps (depth 2, 5 children, 5k-token body, 5 MB import) and the default Budget (D25) with evals.

## 3. Data model

```sql
CREATE TABLE agents (
  id                uuid PRIMARY KEY,
  workspace_id      uuid NOT NULL,
  user_id           uuid NOT NULL,
  project_id        uuid,                -- NULL = everywhere
  name              text NOT NULL,
  description       text NOT NULL,       -- "when to use me"; shown to parents in the delegate tool (D3, D7)
  instructions      text NOT NULL,
  model             text,                -- NULL = "Same as session" (D22)
  tools             text[] NOT NULL,     -- built-in tool names, incl. 'delegate'
  connectors        text[] NOT NULL,     -- Connector Slugs, resolved per project (D21)
  skills            text[] NOT NULL,     -- Skill names, allowlist (D9)
  permission_mode   text NOT NULL CHECK (permission_mode IN ('ask','auto','full-auto')),
  budget            jsonb NOT NULL,      -- tokens, dollars, turns, wall clock
  result_cap_tokens int  NOT NULL DEFAULT 2000,  -- context.md Decision 6
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE skills (
  id           uuid PRIMARY KEY,
  workspace_id uuid NOT NULL,
  user_id      uuid NOT NULL,
  project_id   uuid,                 -- NULL = everywhere
  name         text NOT NULL,
  description  text NOT NULL,
  body         text NOT NULL,        -- ≤ 5k tokens (D10)
  files_ref    text,                 -- blob_ref of the other files (scripts/, references/, assets/); NULL if none
  has_scripts  boolean NOT NULL DEFAULT false,
  source       text NOT NULL CHECK (source IN ('form','import')),
  created_at   timestamptz NOT NULL DEFAULT now(),
  updated_at   timestamptz NOT NULL DEFAULT now()
);
```

- Names (D28): Agent and Skill names follow the Agent Skills rule: 1–64 lowercase letters, digits and hyphens, no leading, trailing or double hyphen. The form shows a label and generates the name ("Trip Planner" → `trip-planner`).
- Default Budget for a new Agent and for General (D25): 500k tokens, $2, 50 turns per user message, 30 min wall clock. Starting values.
- Name uniqueness (D13): within one scope (a given Project, or everywhere for a user), a name is used by at most one Agent or Skill, and never by a built-in Slash Command. Enforced by the app, since it spans both tables.
- `projects.default_agent_id` (existing Default Agent concept) points at an Agent visible in that Project.
- An imported Skill is a stored copy; nothing links it to its source (D24).
- **Agent snapshot** (in `session.created` and `session.config_changed`): the Agent's fields after name resolution for the session's Project (§5.1): resolved tool list, resolved Connector Slugs, resolved Skill index (name + description + skill id per entry), mode, Budget, result cap, model (or "same as session").

## 4. Contracts

### 4.1 `delegate` tool

```
delegate(agent: string, task: string, blocking: bool = true) -> result
```

- Present only when the session's Agent allows `delegate`, the session's `depth < 2` (D7), and it is not a System Session (D29).
- Its description lists every Agent visible in the Project with its `description` (D7), and says: "The child sees none of this conversation. Put every fact it needs in `task`." (D5)
- Errors, returned as tool errors before any child is created:
  - Unknown agent: `no agent named "<agent>"`.
  - Concurrency: `child limit reached (5 running); wait or combine tasks` (D8).
  - Missing Provider Key: `<agent> needs a <Provider> key; add one in settings or change the Agent's model` (D22).
- Blocking: the tool result is the child's capped result (§5.4). Non-blocking: the tool result is immediate (child id); the result arrives later as `child.completed` (event-log.md Decision 24).
- Approval: asks in ask mode, runs in auto/full-auto (approver.md §5.2).

### 4.2 `load_skill` tool

```
load_skill(name: string) -> Skill body
```

- Accepts only names in the session's Skill index (D9). Unknown name → tool error `no skill named "<name>"`.
- Read-only; never asks, in any mode (D10).
- The body arrives as a tool result, stays in the log, and is re-injected after Compaction (context.md §5.3, 5k-token cap).

### 4.3 Slash Commands

- `/agent <name>`: switch this session's Agent from the next turn (D13, D17).
- `/<skill-name> [text]`: load that Skill, then send `text` as the user message (D10). Works for any Skill visible in the Project, even one not in the Agent's list (D19). Logged as `user.message{…, skill: {skill_id, body}}`; the prompt places the body before the text, and replay uses the stored body, not the current Skill (D26).
- Resolution order for `/<word>`: built-in commands, then Skill names (D13).

### 4.4 Skill import

- Input: a `.zip` (≤ 5 MB) containing a `SKILL.md` at the root or in one top-level folder, or a single `SKILL.md` (D24).
- `SKILL.md` frontmatter per the Agent Skills spec: `name` and `description` are required; `license`, `compatibility`, `metadata` are accepted and not used; `allowed-tools` is ignored (D15).
- Body over 5k tokens → rejected (D10).
- Name already used in that scope → the review screen asks "Replace existing or rename?"; never a silent replace. Replacing affects only new sessions (D14, D29).
- Review screen before saving: full instructions text, a "Contains N scripts (run only in a Sandbox)" warning when there are scripts (D24), and the `allowed-tools` note when present (D15).

## 5. Algorithms and flows

### 5.1 Name resolution (D1, D21)

At session start (and on `/agent`), for the session's Project:
1. Agent: the project-scoped Agent with that name, else the everywhere one.
2. Each name in `skills`: project Skill, else everywhere Skill, else dropped.
3. Each slug in `connectors`: the Project's Connector with that slug, else dropped.
4. The result is the Agent snapshot (§3). Nothing missing fails the session; the Agent form shows e.g. "notion: not in this project".

### 5.2 Delegation

1. The parent's model calls `delegate(agent, task, blocking)`.
2. The Worker checks, in order: depth, agent name, running-child count < 5, Provider Key for the Agent's model. Any failure → tool error (§4.1), no child.
3. Otherwise, one tx (event-log.md §5.10): `child.started{child_session_id, agent, task, blocking}` on the parent, `session.created{snapshot, parent_id, depth+1}` for the child, and, if blocking, park the parent `awaiting_children`.
4. Several `delegate` calls in one turn run as parallel children; a blocking parent waits for all its blocking children (D6).

### 5.3 Child start context (D5)

The child's prompt uses the normal layout (context.md §5.2):
- system: platform text + the child's Agent instructions + Project instructions;
- layer 3: memory index (view only, memory.md Decision 4), Project Files index, the child's Skill index;
- first user message: `task`.

It never contains the parent's conversation, tool results, or loaded Skills.

**Jev in a child** (D27): Jev's user-message input is the root session's real user messages, not the `task`; the task counts as agent-written text, which Jev never sees (approver.md §5.4).

### 5.4 Result and cap (D12)

1. The child's instructions tell it to end with a distilled result under its Agent's `result_cap_tokens`.
2. At `session.completed`, if the final message fits the cap, it is the result.
3. Otherwise, the full final message is saved as an Artifact on the child, and the result is the first ~cap tokens plus the Artifact link. No extra LLM call.
4. `child.completed{outcome, summary, usage totals}` carries the result to the parent.

### 5.5 Budget roll-up (D11)

- The child runs under its own snapshot Budget, like any session.
- In `FinishChild`'s tx, the parent's `tokens_used` and `cost_micros` are increased by the child's totals. Turns and wall clock don't roll up.
- The parent's normal Budget check (event-log.md §5.9) runs before its next turn, so a parent pushed over its limit by children pauses then.
- The `usage` table is unchanged: the child's Usage stays recorded on the child only, so nothing is counted twice.

### 5.6 Shared Sandbox (D20)

- A child uses its parent's Sandbox if one is attached.
- A child that needs one and there is none requests it through the normal Sandbox approval (on the child's card); once attached, it belongs to the parent, so later children and the parent share it.
- Parallel children may write the same files; this is accepted.

### 5.7 Stop a child (D23)

- Stop on a child card sends an Interrupt to that child only.
- The child ends; the parent gets `child.completed{outcome: interrupted}`. A blocking parent wakes when it was the last open blocking child.

### 5.8 `/agent` switch (D17, D18)

1. Resolve the Agent (§5.1). Missing Provider Key → show the D22 message; no event.
2. Append `session.config_changed{agent_id, snapshot, mode}`, where `mode` is the new Agent's saved mode (a `/mode` override is dropped).
3. From the next turn: new instructions, tools, Skill index, Budget limits. Tokens and dollars already used carry over. Skills already loaded stay in the log.

## 6. Rules and invariants

- A Child Session never sees its parent's conversation (D5).
- `depth ≤ 2`; a session at depth 2 has no `delegate` tool (D7).
- At most 5 running children per session (D8).
- A Skill body enters the prompt only via `load_skill` or `/skill-name`; never at session start (D16).
- The model sees only Skills in its Agent's list (D9).
- A Skill never pre-approves anything; only Approval Rules and the mode table decide (D15, ADR 0004).
- Skill scripts run only in the Sandbox (D4, ADR 0003).
- A running session's Agent config comes only from `session.created` and later `session.config_changed` events (D14, D18).
- A project's Default Agent can't be deleted; General can't be deleted (D14, D15).
- A child's result to the parent never exceeds its cap (context.md Decision 6).
- System Sessions never have the `delegate` tool (D29).
- Jev never treats a `delegate` task as user intent (D27).

## 7. Events

| Type | Payload | Who writes | Fenced | Status effect |
|---|---|---|---|---|
| `session.config_changed` | agent_id?, snapshot?, model?, mode? (at least one) | API | no | — ; `Fold` applies them in order, latest wins; `agent_id` also updates `sessions.agent_id` |

Existing events used: `session.created` (snapshot), `child.started`, `child.completed`, `session.completed`, `tool.call.*` (for `delegate`, `load_skill`), `artifact.saved` (over-cap results).

## 8. UI

- **Agent form** (project settings → Agents, and Settings → Agents for everywhere): name, description, instructions, model (incl. "Same as session"), tools (checkboxes, incl. "Can delegate"), Connectors, Skills (checkboxes), Permission Mode, Budget, result cap. Hints for names missing in the current Project (D21).
- **Skill form** (same two places): name, description, instructions. **Import** button: `.zip` / `SKILL.md`, then the review screen (§4.4).
- **Child card** in the parent's chat: Agent name, task, status, expand to stream, Stop (D23). Its approvals open on its own card (approver.md Decision 14).
- **Slash menu** lists built-ins, then Skills; `/agent` opens an Agent picker.

## 9. Decisions

All accepted 2026-09-27 (grilling Q0–Q23).

1. **Scope**: Agents and Skills are saved in one Project or everywhere; a project one wins on a name clash.
2. **Creation**: forms for everyone, no admin gate; Skills also importable as `.zip` / `SKILL.md`. No git or YAML editing.
3. **Agent fields**: description (required), instructions, model, tools, Connectors, Skills, Permission Mode, Budget, result cap.
4. **Skill scripts** use the existing Sandbox path: files copied in when a Sandbox is attached; without one, request it (normal approval) or follow instructions only.
5. **Fresh child context**: own Agent instructions, task, Project instructions, Project Files index, memory index. No parent conversation, no fork.
6. **`delegate(agent, task, blocking=true)`**; parallel = several calls in one turn.
7. **Delegation targets**: every Agent visible in the Project, including itself and General; needs `delegate` in the Agent's tools. Max depth 2, platform-set; a child at the cap has no `delegate`.
8. **Concurrency**: at most 5 running children per session; beyond that, a tool error.
9. **Agent `skills` = allowlist**; General gets every visible Skill.
10. **Skill loading**: prompt carries name + description; `load_skill(name)` (never asks) or `/skill-name [text]`; loaded bodies stay and survive Compaction; body cap 5k tokens.
11. **Budget roll-up**: child tokens and dollars are added to the parent's counters on `child.completed`; the child's own Budget still applies.
12. **Result cap enforcement**: instruct the child; over the cap → full text as an Artifact, parent gets the first ~cap tokens plus the link. No extra LLM call.
13. **Slash Commands**: `/agent <name>` switches; `/<skill-name> [text]` loads; agents get no `/<name>`; built-ins win; names unique across Agents and Skills within a scope. No marketplace.
14. **Snapshots**: running sessions keep their snapshot; edits reach new sessions and `/agent`. A project's Default Agent can't be deleted.
15. **`allowed-tools` is ignored**, with a note on import. **General**: all built-in tools incl. `delegate`, all Connectors, all visible Skills, auto mode, default Budget, user's default model; editable, not deletable.
16. **No always-on Skills.** Only name + description go in the prompt; unlisted Skills appear nowhere.
17. **`/agent` mid-session**: spent tokens and dollars carry over, limits come from the new Agent, mode resets to its default.
18. **One event for switches**: `session.config_changed{agent_id?, snapshot?, model?, mode?}` for `/agent`, `/model` and `/mode`.
19. **`/skill-name` for an unlisted Skill is allowed**; the list controls only what the model sees.
20. **Children share the parent's Sandbox**; a child attaching one attaches it to the parent.
21. **Names resolve at session start**; anything missing is absent, with a hint in the Agent form.
22. **Missing Provider Key**: `delegate` and `/agent` fail with a clear message; the model field offers "Same as session".
23. **Child control**: Stop per child card (interrupts that child only; parent gets `outcome: interrupted`). No direct steering.
24. **Import safety**: an imported Skill is a stored copy, never updated from its source; review screen shows the instructions and a script warning; 5 MB zip limit.

Accepted 2026-09-27 (open-gap round, Q24–Q30):

25. **Default Budget**: 500k tokens, $2, 50 turns per user message, 30 min wall clock, for new Agents and General; children use their own Agent's values. Starting values, tuned with evals.
26. **`/skill-name` logging**: a `skill: {skill_id, body}` field on `user.message`; the body is stored with the event so replay ignores later edits.
27. **Jev in a child** sees the root session's user messages, never the `task` as user intent.
28. **Name rules**: Agent and Skill names use the Agent Skills charset (lowercase letters, digits, hyphens, ≤ 64); the form generates them from a label.
29. **Import name clash**: ask "Replace existing or rename?"; never silent. **System Sessions** never get `delegate`.
30. **No test run in the Agent form** in v1.

## 10. Edge cases

- **Parent at depth 1 delegates to an Agent that allows `delegate`**: the grandchild (depth 2) gets no `delegate` tool.
- **6 `delegate` calls in one turn**: 5 children start; the 6th gets the concurrency error and the model can retry later.
- **$1 parent Budget, two children spending $0.60 each**: after both complete, the parent's dollars are $1.20 + its own; the next turn check raises `budget.exceeded`.
- **Child result of 5,000 tokens, cap 2,000**: an Artifact on the child holds the full text; the parent sees ~2,000 tokens plus the link.
- **Agent edited while a child using it runs**: the child keeps its snapshot.
- **Project has no `notion` Connector; the everywhere Agent lists it**: the session starts without Notion tools.
- **Project Skill `summarize` and everywhere Skill `summarize`**: the project one is used.
- **User types `/sql-helper` in a "Kids tutor" session that doesn't list it**: it loads for this session.
- **`/agent Researcher` after `/mode full-auto`**: mode becomes Researcher's saved mode.
- **`/agent` to an Agent whose model needs a missing key**: error message, no event, Agent unchanged.
- **Child needs a Sandbox, parent has none**: the approval shows on the child's card; once attached, the parent and later children use it.
- **Two parallel children edit the same file**: last write wins; no locking.
- **Stop on one of two blocking children**: the parent stays `awaiting_children` until the other completes.
- **Import with `allowed-tools: Bash(git:*)`**: saved; the field has no effect; the review screen shows the note.
- **Import of a Skill whose body is over 5k tokens**: rejected.
- **Deleting an Agent that is some project's Default Agent**: refused.
- **Web page tells the parent to have a child email a file out**: the task contains it, but the child's Jev judges against the root user's messages, so the send is asked or denied.
- **Skill edited after `/summarize` was used**: replay of that session still uses the body stored on the `user.message`.
- **Importing `summarize` when one exists in that scope**: the user must pick Replace or Rename.

## 11. Acceptance criteria

- A project Agent and an everywhere Agent with the same name: new sessions in that Project use the project one; other Projects use the everywhere one.
- A session's first prompt contains each listed Skill's name and description and no Skill body.
- `load_skill("x")` returns the body as a tool result, never produces an approval, and the body is present after a Compaction.
- `delegate` from depth 2 is impossible (tool absent); a 6th concurrent child returns the concurrency error and creates no session.
- A child's first prompt contains its Agent instructions, Project instructions, memory index, Project Files index, its Skill index and the task, and none of the parent's messages.
- After a child with usage (T tokens, D dollars) completes, the parent's `tokens_used`/`cost_micros` rise by T/D in the same tx as `child.completed`; the `usage` table has the child's rows only.
- A child final message over its cap yields an `artifact.saved` on the child and a parent result ≤ cap plus the link.
- `/model`, `/mode` and `/agent` each append one `session.config_changed`; replay after a crash reproduces the switched model, mode and Agent.
- `/agent` resets the mode to the new Agent's saved mode and keeps spent tokens and dollars.
- An imported `.zip` over 5 MB, without `name`/`description`, or with a body over 5k tokens is rejected; a valid one is saved as a copy with `has_scripts` set correctly.
- Stop on a child card interrupts only that child; the parent log gets `child.completed{outcome: interrupted}`.
- Deleting General or a project's Default Agent is refused.
- A new Agent's Budget defaults to 500k tokens / $2 / 50 turns / 30 min.
- `/x hello` appends one `user.message` with `skill.body`; replay after editing Skill `x` rebuilds the original prompt.
- A child's `JevInput` contains the root session's user messages and not the `task`.
- Tidy memory sessions have no `delegate` tool.
- An Agent label "Trip Planner" saves as `trip-planner`; `Trip_Planner` typed as a raw name is rejected.

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

Prior-art survey (Claude Agent SDK, OpenAI Agents SDK, Gemini CLI, Gemini Gems, Claude Projects, ChatGPT GPTs), the Agent Skills spec, skill supply-chain incidents (ToxicSkills, ClawHavoc) and sources: [../research/agents-skills.md](../research/agents-skills.md).
