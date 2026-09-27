# Approver and Permissions: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): the step that decides allow/ask/deny is the **Approver**; a saved "always allow" is an **Approval Rule**, scoped to a **Project** or everywhere, never created implicitly; **Jev** is the platform-provided decision model the Approver calls after rules, shown to users only as "auto mode"; **Permission Mode** is ask/auto(default)/full-auto; a paused decision is an **Approval**. Consistent with ADR [0003](../adr/0003-nothing-executes-on-the-host.md) (shell/file tools are Sandbox-only; no mode-table row for them yet), ADR [0004](../adr/0004-layered-approver-no-implicit-rules.md) (layered Approver, hints seed defaults, suggest-never-auto-create), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (Jev is a metered Platform Service, Quotas exist from day one), [agent-loop.md](agent-loop.md) (`Hook.BeforeTool`, `Approver.Decide`, `approved_by`/trace on `tool.call.started`, §5.2 ask-gates-the-batch — replaced here by §5.8, Decision 18: a failed check → ask), [event-log.md](event-log.md) (`approval.requested`/`.resolved`, `awaiting_approval` holds no Worker), [mcp-client.md](mcp-client.md) (Decision 12 rug-pull `tool_hash`, Decision 18 hint trust tiers). Background and prior-art survey: [../research/approver.md](../research/approver.md).

## 1. Summary

- The Approver decides allow, ask, or deny for every gated tool call, in a fixed order: the rug-pull exception, then Approval Rules, then Jev, then the user. There is no small-LLM fallback — if Jev is unsure or unavailable, the Approver asks the user directly (Decision 1, 10).
- Jev's input is limited to the tool call (name + arguments), the tool's flags, and the user's own messages — never tool results, Connector content, memory the agent wrote, or the agent's own text between tool calls, so a hijacked agent can't talk its way past it (Decision 2).
- If Jev doesn't answer within 5 seconds, or the platform's Jev Quota is used up, the call becomes an ordinary ask; the UI never names Jev in that messaging (Decision 3, 4, 5).
- What runs free, what always asks, and what goes through Approval Rules → Jev depends on the tool class and the session's Permission Mode — the mode table in §5.2 (Decision 7).
- A Permission Mode comes from the Agent's saved default, can be overridden per session with `/mode`, and a Child Session takes the stricter of its parent's mode and its own Agent's mode (Decision 8).
- Approval Rules are allow-only: an exact tool name, optional argument patterns, scoped to one Project or everywhere. There are no deny rules, they're checked before the model ever runs, and the model never sees them (Decision 9).
- A Connector tool whose definition changed since the Connector was added or the change was last approved (the rug-pull `tool_hash`, mcp-client.md Decision 12) always asks again, in every mode, even when a rule matches — the only exception to a mode's normal behavior so far (Decision 6).
- After 3 approvals of the same tool and scope within 7 days, the Approver may suggest a rule; it never creates one automatically (Decision 11, ADR 0004).
- Approvals never expire; a session waiting on one holds no Worker (Decision 12).
- A Jev deny blocks the call with an inline "Auto mode blocked…" line and skips the later calls in its batch; after 3 in a row, the next becomes an ask. "Approve all" approves once only; rules are managed (delete-only) in Settings/Project settings → Permissions (Decisions 19–23).
- A batch of pending calls that need asking opens one stepped card per session ("1 of 3"), resolved one call at a time; a deny skips the calls after it. This replaces agent-loop.md §5.2's all-or-nothing "ask-gates-the-batch" (Decision 13). A Child Session gets its own card, labelled with its Agent name (Decision 14).
- Jev's allow/ask confidence threshold is fixed per tool class (read-only/write/destructive) by the platform and tuned with evals; users never see the number. Built-in tools carry no `tool_hash` — they ship with the binary (Decision 15, 16).

## 2. Scope

**In the base version**
- The Approver as `agent-loop.md`'s `Hook.BeforeTool`/`Approver.Decide` for `kind: tool` approvals only (`budget` and `sandbox` kinds belong to event-log.md and are not re-specified here).
- The `approval_rules` table and its two creation paths: a stepped-card scope choice, and accepting a rule suggestion.
- Rule-suggestion detection (3 approvals of the same tool + scope within 7 days), computed from the Event Log.
- Mode resolution: Agent default, `/mode`, the Child Session stricter-of rule.
- Jev integration: the input contract, the fixed 5 s timeout, Quota-exhaustion handling, per-tool-class thresholds.
- The stepped approval card: one card per session, per-call resolution, "Approve all", Child Session cards.
- Recording `reason: jev_unavailable` / `reason: jev_quota_exhausted` on the approver trace.

**Out (deferred)**
- The small-LLM fallback mentioned in ADR 0004 and agent-loop.md §4.4 — removed by Decision 1; this spec's chain (rules → Jev → user) supersedes that wording.
- Shell and file tools — they don't exist until the Sandbox (ADR 0003), so the mode table has no row for them.
- Compound shell command splitting and any other shell-specific Approver handling — moved to the Sandbox item in TODO.md (Decision 17).
- Any full-auto exception besides the rug-pull one (Decision 6) — none decided yet.
- Editing an Approval Rule: rules are delete-only; recreate one by approving again (Decision 23).
- The Quota reset period and date: defined in [usage-metering.md](usage-metering.md) (monthly, 1st, UTC).

## 3. Data model

```sql
CREATE TABLE approval_rules (
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users,   -- Approval Rules are owned by the User (CONTEXT.md)
  project_id    uuid REFERENCES projects,          -- NULL = everywhere (Decision 9)
  tool_name     text NOT NULL,                     -- exact match; Connector Slug + tool (e.g. notion__create_page) or a built-in tool name
  arg_patterns  jsonb,                              -- optional, per argument: exact value or glob (Decision 9); an argument not listed is unconstrained
  created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX approval_rules_lookup ON approval_rules (user_id, tool_name);
```

- Allow-only: there is no `decision` column. A row's existence *is* the allow (Decision 9).
- No hash on rules: the rug-pull baseline lives on `connector_tools.tool_hash`, taken when the Connector is added to the Project (mcp-client.md Decision 12, §5.6).
- No new table for Approvals or their resolution: `approval.requested`/`approval.resolved` (event-log.md §4) already carry that; this spec only adds meaning to their existing fields (§7).

## 4. Contracts

```go
package approver

type Mode string // "ask" | "auto" | "full-auto" (CONTEXT.md: Permission Mode)

// Strictness order, least to most permissive (Decision 8): ask < auto < full-auto.
var modeStrictness = map[Mode]int{"ask": 0, "auto": 1, "full-auto": 2}

type ApprovalRule struct {
    ID          uuid.UUID
    UserID      uuid.UUID
    ProjectID   *uuid.UUID        // nil = everywhere
    ToolName    string
    ArgPatterns map[string]string // key = argument name; value = exact string or glob
    CreatedAt   time.Time
}

type RuleStore interface {
    // Match returns the Approval Rule that allows this call, if any: an exact
    // tool-name match, project-scoped or everywhere, whose arg_patterns (if
    // any) all match the call's arguments (Decision 9).
    Match(ctx context.Context, userID, projectID uuid.UUID, call msg.ToolCall) (*ApprovalRule, error)
    Create(ctx context.Context, r ApprovalRule) error
}

// JevClient is called only for the mode-table cells marked "rules → Jev" (§5.2),
// and only after the rug-pull check passes and no rule matches.
type JevClient interface {
    Check(ctx context.Context, in JevInput) (JevVerdict, error) // ctx carries the fixed 5s deadline (Decision 3)
}

type JevInput struct {
    ToolName    string          // e.g. notion__create_page
    Args        json.RawMessage
    ReadOnly    bool            // ToolDef.ReadOnly (agent-loop.md §3)
    Destructive bool            // ToolDef.Destructive
    Trusted     bool            // hint source trusted: built-in, or a catalog Connector (mcp-client.md Decision 18)
    UserTexts   []string        // this session's own user.message text only (Decision 2)
}

type JevVerdict struct {
    Decision   Decision // Allow | Ask | Deny
    Confidence float64
}

type Decision string // "allow" | "ask" | "deny"

// ModeResolver implements Decision 8.
type ModeResolver interface {
    // Resolve returns the effective mode for a session: its own current mode
    // (Agent default, or a /mode override for this session), or, for a Child
    // Session, the stricter of that and the parent's effective mode.
    Resolve(ctx context.Context, sessionID uuid.UUID) (Mode, error)
}

// Approver composes the above into agent-loop.md's Hook.BeforeTool / Approver.Decide.
// Not a new interface — this is what implements the one agent-loop.md already defines.
type Approver struct {
    Rules RuleStore
    Jev   JevClient
    Modes ModeResolver
}
```

## 5. Algorithms and flows

### 5.1 Evaluation order (Decision 10)

For every `kind: tool` call (step 1 runs in every mode; steps 2–4 only where §5.2's mode table doesn't already let the call run or ask):

1. **Rug-pull exception** (§5.6): for a Connector tool whose current definition no longer matches `connector_tools.tool_hash`, ask — skip the rest. This runs in every mode, before the mode table, even for calls that would otherwise run free.
2. **Approval Rules** (§5.5): an exact tool-name match, project-scoped or everywhere, whose argument patterns (if any) match → allow, `approved_by: rule:<id>`. Skips Jev.
3. **Jev** (§5.4), only where the mode table routes through it: allow or deny at or above threshold; ask below threshold, on error, on timeout, or while the Quota is exhausted.
4. **User**: the stepped approval card (§5.8).

### 5.2 Mode table (Decision 7)

| Tool class | ask | auto | full-auto |
|---|---|---|---|
| read-only built-in (web search, web fetch, memory view) | runs | runs | runs |

In the base version web search and web fetch are the Provider's built-in tools: it runs inside the LLM call and never reaches the Approver ([provider-gateway.md](provider-gateway.md) D6a).
| catalog Connector tool with `readOnlyHint` | asks | runs | runs |
| other catalog Connector tools | asks | rules → Jev | runs |
| custom-URL Connector tools (hints ignored) | asks | rules → Jev | runs |
| memory writes, delegate | asks | runs | runs |

Shell and file tools don't exist until the Sandbox (ADR 0003) — there is no row for them yet.

### 5.3 Mode resolution (Decision 8)

1. An Agent's saved Permission Mode is the default for every new session using it.
2. `/mode` changes only the current session; it never rewrites the Agent's saved default.
3. A Child Session's effective mode is the stricter (lower in `modeStrictness`, §4) of its parent session's effective mode and its own Agent's saved mode.
4. `/mode` is logged as `session.config_changed{mode}`. `/agent` logs `session.config_changed{agent_id, snapshot, mode}` with the new Agent's saved mode, dropping any `/mode` override ([agents-skills.md](agents-skills.md) D17–D18).

### 5.4 Jev check (Decision 2, 3, 4, 5, 15)

- **Input**: only `JevInput` (§4) — the tool call, its `ToolDef` flags, and this session's own user messages. Never tool results, fetched or Connector content, memory the agent wrote, or the agent's own text between tool calls (that text is what a hijacked agent writes). This follows the Claude Code auto mode design and the lesson of CVE-2026-52024.
- **Child Sessions**: "this session's own user messages" means the root session's user messages; a `delegate` task is agent-written text and is never passed as user intent ([agents-skills.md](agents-skills.md) D27).
- **Timeout**: 5 s, a fixed platform setting. No answer in that window → the call becomes an ordinary ask. The approver trace on `approval.requested` records `reason: jev_unavailable`, and Jev is not retried for that specific call — a later, different call in the same session still tries Jev normally.
- **Quota exhausted**: every call that would otherwise go to Jev instead asks the user, for the rest of the Quota period. One banner is shown per session, not per call (§8).
- **Thresholds**: fixed by the platform, one per tool class (read-only / write / destructive — the `ToolDef.ReadOnly`/`Destructive` flags, not the five routing classes of §5.2), tuned via the eval suite. Never shown to users.
- **Jev deny** (Decision 19, 20): the call is blocked and never runs. The model gets `tool.call.completed{is_error: true, denied: true}` with "blocked by auto mode: <short reason>" and can try another way. The chat shows one inline line ("Auto mode blocked `gmail__send`: <reason>"), with no push notification and no "Allow anyway" button. Later calls in the same batch are skipped, as after a user deny (§5.8 step 7). After 3 Jev denies in a row in one session (tuned via evals), the next call Jev would deny becomes an ordinary ask (stepped card) instead; any call Jev doesn't deny, or any user answer, resets the streak.
- **Naming**: "Jev" never appears in the UI. Everywhere a user sees this step — including an `approved_by: jev` chip — it reads "auto mode".

### 5.5 Approval Rule matching (Decision 9)

- Match key: the exact tool name (a built-in tool's name, or `<Connector Slug>__<tool>`).
- Optional argument patterns: one exact value or glob per argument named in the rule; an argument the rule doesn't mention is unconstrained.
- Scope: one Project, or everywhere (`project_id IS NULL`).
- Allow-only — there are no deny rules.
- Checked by our code in `Hook.BeforeTool`, before the tool ever runs. Rules are never placed in the model's prompt; the model never sees them.
- A match allows the call and skips Jev.

### 5.6 Rug-pull exception (Decision 6)

Hash mechanics (computing and comparing `tool_hash`) are defined in [mcp-client.md §5.6](mcp-client.md#56-rug-pull-check-decision-12); this spec adds only the mode interaction: the check runs regardless of Permission Mode, against the baseline taken when the Connector was added to the Project. Even where §5.2's table would otherwise let the call run free (full-auto), a rule matches, or the call would route to Jev (auto), a stale hash forces an ask, with the message mcp-client.md §5.6 already defines ("This tool changed since you approved it"). This is the only exception to a mode's normal behavior right now; there are no shell exceptions until the Sandbox exists. Approving the call stores the new hash (mcp-client.md Decision 12, amended 2026-09-27).

### 5.7 Rule suggestion (Decision 11)

After 3 `approval.resolved{decision: allow}` events for the same tool name and the same scope (Project or everywhere) within a rolling 7-day window, the Approver may surface a suggestion to create that Approval Rule. The count and window are tunable via evals. It is only a suggestion: accepting it runs the same `RuleStore.Create` call a stepped-card scope choice makes (§5.8); declining does nothing. A rule is never created without this explicit action (ADR 0004).

### 5.8 Stepped approval card (Decision 13, 14 — replaces agent-loop.md §5.2's "ask-gates-the-batch")

1. `exec` runs the Approver (§5.1) on every pending call in the turn's batch before starting any (agent-loop.md Decision 2, unchanged). Each call ends allow, ask, or Jev deny. A Jev deny skips every call after it in the batch (§5.4); any ask calls before it still go through the card.
2. If every call allows, the batch runs exactly as agent-loop.md §5.2 already describes — no card.
3. If any call asks: `exec` appends `tool.call.requested` for the whole batch as before, then `approval.requested` for the first ask call in the model's order, and parks `awaiting_approval`. (event-log.md's `OpenApproval` is one call at a time — exactly what a stepped card needs; no change to that state shape.)
4. The UI opens one card for the session, showing the current pending call and a counter ("1 of N", N = the number of ask calls in this batch), with **Approve once** / **Approve for this project** / **Approve everywhere** / **Deny** (optional reason), plus **Approve all**, which approves every remaining ask call with scope `once` (never creates a rule) without stepping through them one by one (Decision 21).
5. Each answer is `approval.resolved{decision, scope, by}` (event-log.md §4). "Approve for this project" or "Approve everywhere" also creates an Approval Rule (§5.5) before the session resumes.
6. On the next claim, if another ask call in this batch is still unresolved, `Decide` requests its approval the same way (step 3) — the card advances ("2 of N", …).
7. A **Deny** closes the card: every call after it in the batch is skipped, including calls already allowed by a rule or Jev, and none gets its own `approval.requested`. Each ends `tool.call.completed{is_error: true}` with "skipped because an earlier call was denied." The denied call itself ends `tool.call.completed{is_error: true, denied: true}` with "denied by user: <reason>." There is no separate "Deny all" — denying the current call has that effect.
8. Once every ask call is resolved or skipped, the whole original batch — allowed calls and card-approved calls together — runs in the model's order (agent-loop.md §5.2's `ParallelSafe` grouping still applies within it).
9. The model gets a result for every call it requested: the real result, the denial text, or the skip text. It replans from there.

**Child Sessions** (Decision 14): a Child Session's own pending calls open their own card on the Child Session, not folded into the parent's, labelled with the child's Agent name.

## 6. Rules and invariants

- Approval Rules are checked by code in `Hook.BeforeTool`; they are never in the model's prompt (Decision 9).
- There are no deny rules; a rule's existence is itself the allow (Decision 9).
- A rule match skips Jev (Decision 9).
- Jev's input never includes tool results, fetched or Connector content, memory the agent wrote, or the agent's own text between tool calls (Decision 2).
- A failed Approver check — Jev timeout, error, or Quota exhaustion — always resolves to ask, never allow (Decision 3, 4; agent-loop.md Decision 18).
- Jev is never retried for a call that already fell back to asking on timeout (Decision 3).
- Approvals never expire; a session waiting on one holds no Worker (Decision 12; event-log.md §5.7, §6).
- The UI never names Jev; it is always "auto mode," including on the `approved_by: jev` chip (Decision 5).
- An Approval Rule is only ever created by an explicit user action — a stepped-card scope choice or accepting a suggestion — never automatically (Decision 11; ADR 0004).
- A Connector tool call whose definition changed since the stored baseline always asks again, in every mode, even when a rule matches (Decision 6).
- Built-in tools have no hash; they ship with the binary (Decision 16).
- A deny in the stepped card skips, not denies, the calls after it — they never run and never get their own `approval.requested` (Decision 13). A Jev deny does the same (Decision 20).
- "Approve all" never creates an Approval Rule (Decision 21).
- A Child Session's Permission Mode is never looser than its parent's (Decision 8).
- Shell and file tools have no mode-table row; they don't exist until the Sandbox (ADR 0003).

## 7. Events

No new event types. This spec fixes what the existing catalog rows (event-log.md §4) carry for `kind: tool` Approvals.

| Event | What this spec adds |
|---|---|
| `approval.requested` | one per ask-verdict call, not one per batch — event-log.md's `tool_call_id` field already supports this. The approver trace gains `reason: jev_unavailable` (Decision 3) or `reason: jev_quota_exhausted` (Decision 4) when either applies. |
| `approval.resolved` | issued one at a time by the stepped card (§5.8), not once for the whole batch. `scope: project`/`everywhere` also creates an Approval Rule (§5.5). |
| `tool.call.started` | `approved_by: jev` is rendered in the UI as "auto mode" (Decision 5), unchanged shape (agent-loop.md Decision 3). |
| `tool.call.completed` | a skipped call carries "skipped because an earlier call was denied"; a denied call carries "denied by user: <reason>" (Decision 13); a Jev-denied call carries `denied: true` and "blocked by auto mode: <short reason>" (Decision 19). |

## 8. UI

- **Stepped approval card** (§5.8): "1 of N," per-call Approve once / Approve for this project / Approve everywhere / Deny with an optional reason, plus Approve all (Decision 13).
- **Child Session card**: same shape as above, labelled with the child's Agent name (Decision 14).
- **Jev Quota banner**: one per session, shown while the session's Jev-eligible calls are instead all asking; never names Jev, e.g. "Auto mode can't check actions right now, so I'll ask you before each one until Oct 1." The date is the Quota reset (1st of next month, 00:00 UTC) in the user's local date ([usage-metering.md](usage-metering.md) §5.3) (Decision 4, 5, 22).
- **`approved_by` chip**: reads "auto mode," never "Jev" (Decision 5; agent-loop.md §8).
- **Auto mode blocked line**: one inline chat line per Jev deny, e.g. "Auto mode blocked `gmail__send`: sending to an address you never mentioned." No push, no "Allow anyway" (Decision 19).
- **Permissions settings** (Decision 23): Settings → Permissions lists everywhere-scoped rules; Project settings → Permissions lists that Project's rules. Each row shows the tool, argument patterns, and created date, with Delete. No edit.
- **Rug-pull re-ask**: reuses mcp-client.md §8's "This tool changed since you approved it." (Decision 6; mcp-client.md Decision 12).

## 9. Decisions

All decided 2026-09-27.

1. **No small-LLM fallback.** The Approver checks Approval Rules, then Jev. If Jev is unsure (below threshold) or unavailable, it asks the user. Supersedes the "small-LLM fallback" wording in ADR 0004 and agent-loop.md §4.4.
2. **Jev's input contract.** Only the tool call (name + arguments), the `ToolDef` flags (read-only, destructive, trusted or untrusted source), and the user's own messages. Never tool results, fetched or Connector content, memory the agent wrote, or the agent's own text between tool calls (a hijacked agent writes that text). Follows the Claude Code auto mode design and the lesson from CVE-2026-52024.
3. **Jev down, or no answer within 5 s** (a fixed platform setting): the call becomes an ordinary ask. The event records `reason: jev_unavailable`, and Jev is not retried for that call.
4. **Jev Quota used up**: every call that would go to Jev asks the user until the Quota resets. One banner per session, not per call. User-facing text never names Jev, e.g. "Auto mode can't check actions right now, so I'll ask you before each one until your limit resets."
5. **"Jev" is an internal name.** The UI calls it "auto mode", including anything shown for `approved_by: jev`.
6. **Full-auto exception: rug-pull.** A Connector tool whose definition changed (the rug-pull `tool_hash`, mcp-client.md Decision 12) always asks again, in every mode, even when a rule matches. There are no other exceptions yet; shell exceptions come later with the Sandbox.
7. **Mode table per tool class** (ask / auto / full-auto) — §5.2. Shell and file tools don't exist until the Sandbox (ADR 0003).
8. **Where the mode comes from**: the Agent's saved mode is the default, and `/mode` changes only the current session. A Child Session gets the stricter of its parent's mode and its own Agent's mode.
9. **Approval Rules: allow-only.** Each is an exact tool name (Connector Slug + tool, e.g. `notion__create_page`), with optional argument patterns (exact value or glob), scoped to a Project or everywhere. No deny rules. Checked in `Hook.BeforeTool` before the call; never in the prompt, and the model never sees them. A rule match skips Jev.
10. **Order**: rug-pull exception → rules → Jev → user.
11. **Rule suggestion**: after 3 approvals of the same tool and scope within 7 days. Tunable via evals. Only a suggestion; rules are never created automatically (ADR 0004).
12. **Approvals never expire.** A waiting session holds no Worker.
13. **Stepped approval card** (replaces agent-loop.md §5.2 "ask-gates-the-batch"): one notification opens one card per session, showing the pending calls one at a time in the model's order ("1 of 3"). Each call: Approve once / Approve for this project / Approve everywhere / Deny, with an optional reason. "Approve all" is available. A deny closes the card, and the later calls are skipped, not run — no separate "Deny all"; denying the current call has that effect. Approved calls run only after the card is done, in the model's order. The model gets a result for every call: the real result, "denied by user: <reason>", or "skipped because an earlier call was denied." Then it replans.
14. **Child Sessions get their own card**, labelled with the child's Agent name.
15. **Jev thresholds are per tool class** (read-only / write / destructive), fixed by the platform and tuned via evals. Users never see them.
16. **No hash for built-in tools**; they ship with the binary.
17. **Compound shell command splitting and shell handling before the Sandbox are out of scope**; they moved to the Sandbox item in TODO.md.
18. **Rug-pull baseline at connect.** Every Connector tool's hash is saved when the Connector is added to the Project (its first `tools/list`), not on Approval Rules, so full-auto is covered. Approving a changed tool stores the new hash. Amends mcp-client.md Decision 12.
19. **Jev deny = blocked.** The call doesn't run; the model gets "blocked by auto mode: <short reason>" and can try another way. The user sees an inline chat line, no push, no "Allow anyway". After 3 Jev denies in a row in one session (tuned via evals), the next one becomes an ordinary ask, so the agent can't loop.
20. **A Jev deny in a batch skips the later calls**, the same as a user deny.
21. **"Approve all" approves once.** It never creates an Approval Rule; saving a rule is always a choice about one specific call.
22. **Quota banner has no date yet**: "until your limit resets". The reset period and date belong to Usage metering (TODO.md). *Resolved 2026-09-27*: monthly, reset 00:00 UTC on the 1st; banner says "until Oct 1" ([usage-metering.md](usage-metering.md) D4).
23. **Managing rules**: Settings → Permissions (everywhere) and Project settings → Permissions (that Project). Each row: tool, argument patterns, created date, Delete. No edit; delete and approve again.
24. **Mode switches are logged** as `session.config_changed`; `/agent` resets the mode to the new Agent's saved mode (2026-09-27, [agents-skills.md](agents-skills.md) D17–D18).
25. **Jev in a Child Session** sees the root session's user messages, never the `delegate` task as user intent (2026-09-27, [agents-skills.md](agents-skills.md) D27).

**Already decided elsewhere** (reference, don't restate as new): `Hook.BeforeTool` batches the whole turn's Approver run before any call starts (agent-loop.md Decision 2); `approved_by`/trace live on `tool.call.started`, no new event type (agent-loop.md Decision 3); a failed Approver check → ask, never allow (agent-loop.md Decision 18); `readOnlyHint`/`destructiveHint` are trusted only for catalog Connectors (mcp-client.md Decision 18); rug-pull hash mechanics (mcp-client.md Decision 12).

## 10. Edge cases

- **Jev times out mid-batch**: that call becomes an ordinary ask and joins the stepped card; a sibling already allowed by a rule or by Jev in the same batch still waits for the card to close (Decision 3, 13).
- **Quota runs out mid-session**: the session's Jev-eligible calls ask for the rest of the Quota period; the banner shows once, not per call (Decision 4).
- **A rug-pull mismatch on a call full-auto would otherwise run free**: asks anyway; no other full-auto exception exists (Decision 6).
- **A Connector tool changes in full-auto with no rule for it**: compared against the baseline taken when the Connector was added; asks once, and approving stores the new hash (Decision 6, 18).
- **A user Denies call 2 of 3 in the stepped card**: call 3 never gets its own `approval.requested`; it's marked skipped, and the card closes after call 2 (Decision 13).
- **A Child Session's own Agent is full-auto, but its parent session is in ask mode**: the child runs in ask mode (Decision 8).
- **A custom-URL Connector tool reports `readOnlyHint: true`**: still asks in ask mode and still goes rules → Jev in auto mode, same as any other custom-URL tool — hints are ignored for it (mcp-client.md Decision 18; §5.2).
- **3 approvals of the same tool land in different Projects**: they don't count toward one suggestion, since the suggestion is scoped by tool + scope together (Decision 11).
- **Jev denies 3 calls in a row**: the next call Jev would deny comes to the user as a stepped card instead, so the agent can't loop on blocked calls (Decision 19).
- **Jev denies call 2 of 3; call 1 needs asking**: call 1 goes through the card; call 3 is skipped (Decision 20).
- **A session sits `awaiting_approval` for weeks**: no Worker or Sandbox is held the whole time (Decision 12; event-log.md §5.7).

## 11. Acceptance criteria

- A tool call with a matching, current-hash Approval Rule in auto mode is allowed with `approved_by: rule:<id>` and never reaches Jev.
- After a Connector tool's schema changes (stale `connector_tools.tool_hash`), the next call asks in every mode, including full-auto and when a rule matches; approving it stores the new hash and the next call follows the normal order.
- A fake `JevClient` that never responds within 5 s produces an ask whose approver trace carries `reason: jev_unavailable`; a later, different call in the same session still calls Jev normally.
- A fake Quota-exhausted signal makes every Jev-eligible call in the session ask, with exactly one banner shown per session.
- A batch of 3 calls where calls 1 and 3 allow (rule/Jev) and call 2 asks: `approval.requested` is appended only for call 2. Denying call 2 turns it into `tool.call.completed{denied:true}` with "denied by user: <reason>", call 1 runs, and call 3 is skipped ("skipped because an earlier call was denied") even though a rule or Jev allowed it.
- A batch of 3 ask-verdict calls: denying call 2 leaves call 3 as `tool.call.completed{is_error:true}` with "skipped because an earlier call was denied," and no `approval.requested` is ever appended for call 3.
- A Child Session whose own Agent default is full-auto, started from a parent session in ask mode, resolves every gated call as ask.
- Three `approval.resolved{decision: allow}` events for the same tool name and scope within 7 days trigger a rule suggestion.
- A fake `JevClient` returning deny at threshold: the call never runs, the model gets "blocked by auto mode: <reason>", the chat shows one inline line, no push is sent, and later calls in the batch are skipped.
- After 3 consecutive Jev denies in a session, the 4th would-be deny produces `approval.requested` instead.
- "Approve all" on a 3-call card yields three `approval.resolved{scope: once}` and creates no Approval Rule.
- Deleting a rule in Settings → Permissions makes the next matching call go through Jev (auto) or ask (ask mode).
- No UI surface renders the string "Jev" to a user; the `approved_by: jev` chip and the Quota banner both read "auto mode" / "Auto mode."

## 12. Open gaps

None. Rule-suggestion count/window, the Jev deny streak (3), and Jev thresholds are tuned with evals, not open design questions.

## 13. Research

Prior-art survey (Claude Code auto mode, Codex CLI, Cursor, Cline/Roo Code, GitHub Copilot, OpenHands, Gemini CLI), rug-pull and confused-deputy security background, and full source list: [../research/approver.md](../research/approver.md).
