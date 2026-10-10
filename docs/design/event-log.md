# Event Log, Run Queue and Leases: Spec

Status: spec, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Background, engine survey, option tables and sources: [../research/event-log.md](../research/event-log.md). What goes into the model's context from the log: [context.md](context.md). Project Memory and User Memory: [memory.md](memory.md).

## 1. Summary

- Each Session has an append-only **Event Log** in Postgres. It is the only source of truth. Every other table (`sessions` status, `usage`, budget counters, memories) is a projection written in the same transaction as the event.
- `seq` is per-session and gapless, from `sessions.last_seq`. It is also the SSE `id`.
- Scheduling unit is the Session. A **Worker** claims a runnable session with a short `FOR UPDATE SKIP LOCKED` transaction, then holds a logical **Lease** (columns, 30 s TTL, 10 s heartbeat). Every Worker append is fenced by `lease_epoch`.
- The Worker never replays code. It folds events into state and calls a pure `Decide(state)` to get the next action. Recovery is the same loop.
- LLM turns are re-run after a crash. Tool calls are never re-run: an unmatched `tool.call.started` becomes an **Interrupted Tool Call** for the model.
- Waiting (Approval, user, children, timers, Budget) is a status in the DB. A waiting session holds no Worker and no Sandbox.
- Token deltas are ephemeral (not stored). Only the final `llm.response` is durable.

## 2. Scope

**In the base version**
- Tables `sessions`, `events`, `usage`, `deletions`; blob storage in object storage for large payload parts.
- `Store.Append` with fencing; claim, heartbeat, worker loop; `Fold` + `Decide`; crash recovery; crash-loop limit.
- Waiting states, Steering, Interrupt, Budget checks, Delegation parking.
- SSE live + replay with `Last-Event-ID`; `DeltaBus` over `pg_notify`.
- Upcasters for payload schema drift.
- Hard delete (keeps anonymized usage totals) and JSON export.
- App-level tenant scoping (`TenantScope`).

**Out (deferred)**
- Postgres RLS (later learning exercise).
- Partitioning `events` (not until ~100M rows; then hash by `session_id` is an option).
- `session_snapshots` (add only if profiling shows folds are slow).
- Fairness/quotas in the claim query (`ORDER BY ready_at` plus a per-user running-count check).
- Redis/NATS `DeltaBus` (swap in only if `pg_notification_queue_usage()` shows pressure).
- Retention policies (sessions live until the user deletes them).

## 3. Data model

```sql
CREATE TABLE sessions (
  id                uuid PRIMARY KEY,
  workspace_id      uuid NOT NULL,
  project_id        uuid NOT NULL,
  user_id           uuid NOT NULL,
  parent_id         uuid REFERENCES sessions(id) ON DELETE CASCADE,
  depth             smallint NOT NULL DEFAULT 0,
  agent_id          uuid NOT NULL,
  status            text NOT NULL CHECK (status IN ('runnable','running','awaiting_approval',
                      'awaiting_user','awaiting_children','sleeping','completed','failed')),
  last_seq          bigint NOT NULL DEFAULT 0,  -- gapless seq counter; also the fence row
  ready_at          timestamptz,                -- when it became runnable (FIFO)
  wake_at           timestamptz,                -- sleeping timers
  lease_owner       text,                       -- worker id; NULL when not leased
  lease_epoch       bigint NOT NULL DEFAULT 0,  -- fencing token; +1 on every claim
  lease_expires_at  timestamptz,
  cancel_requested  boolean NOT NULL DEFAULT false,
  background        boolean NOT NULL DEFAULT false, -- Background Session (projection of session.backgrounded)
  trigger           text NOT NULL DEFAULT 'user_message', -- 'user_message' | 'memory_tidy'; System Session = not 'user_message'
  title             text,                       -- Title (projection of session.renamed); NULL → placeholder from the first user message
  recovery_attempts int NOT NULL DEFAULT 0,     -- claims since the last successful fenced append
  tokens_used       bigint NOT NULL DEFAULT 0,  -- budget counters (projection of usage.recorded)
  cost_micros       bigint NOT NULL DEFAULT 0,
  turns             int    NOT NULL DEFAULT 0,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX sessions_runnable ON sessions (ready_at) WHERE status = 'runnable';
CREATE INDEX sessions_sleeping ON sessions (wake_at) WHERE status = 'sleeping';
CREATE INDEX sessions_expired  ON sessions (lease_expires_at) WHERE status = 'running';
CREATE INDEX sessions_project  ON sessions (workspace_id, project_id, updated_at DESC);
CREATE INDEX sessions_parent   ON sessions (parent_id) WHERE parent_id IS NOT NULL;

CREATE TABLE events (
  session_id     uuid NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
  seq            bigint NOT NULL,             -- per-session, gapless, from 1; SSE id
  workspace_id   uuid NOT NULL,               -- denormalized tenant key
  type           text NOT NULL,               -- domain.action, e.g. tool.call.started
  schema_version smallint NOT NULL DEFAULT 1, -- per type; read through upcasters
  actor          text NOT NULL,               -- user:<id> | worker:<id> | system | child:<session>
  causation_seq  bigint,                      -- event this one reacts to
  correlation_id text,                        -- turn_id | tool_call_id | approval_id | child_session_id
  lease_epoch    bigint,                      -- writer's epoch (audit; NULL for unfenced)
  payload        jsonb NOT NULL,              -- neutral format (ADR 0002); big parts → blob_ref
  created_at     timestamptz NOT NULL DEFAULT now(),  -- DB clock of record
  PRIMARY KEY (session_id, seq)
);
CREATE UNIQUE INDEX events_client_msg ON events (session_id, (payload->>'client_msg_id'))
  WHERE type = 'user.message';                -- idempotent user sends

CREATE TABLE usage (                          -- projection of usage.recorded; survives hard delete
  id           bigserial PRIMARY KEY,
  session_id   uuid,                          -- no FK; set to NULL on hard delete
  seq          bigint,                        -- source event; set to NULL on hard delete
  workspace_id uuid NOT NULL,
  project_id   uuid NOT NULL,
  user_id      uuid NOT NULL,
  kind         text NOT NULL,                 -- llm | jev | search | sandbox_cpu | …
  provider     text NOT NULL,                 -- anthropic | openai | gemini | jev | brave | tavily (usage-metering.md D5)
  model        text,                          -- LLM rows only
  quantity     numeric NOT NULL,
  unit         text NOT NULL,
  cost_micros  bigint,
  created_at   timestamptz NOT NULL,
  UNIQUE (session_id, seq)                    -- one row per usage.recorded
);

CREATE TABLE deletions (                      -- tracks async cleanup; no content
  id           uuid PRIMARY KEY,
  workspace_id uuid NOT NULL,
  session_ids  uuid[] NOT NULL,               -- root + all descendants
  requested_at timestamptz NOT NULL DEFAULT now(),
  completed_at timestamptz
);
```

**Blobs** (object storage, no table). Any payload part that would make the inline payload exceed 32 KB (tool results, tool args, file reads, screenshots) is stored at `ws/{workspace_id}/sess/{session_id}/blobs/{sha256}` and replaced inline by:

```json
{"blob_ref": {"key": "ws/…/sess/…/blobs/9f2c…", "size": 81234, "sha256": "9f2c…", "mime": "text/plain"},
 "preview": "first 2 KB of the content"}
```

Upload the blob before appending the event that references it.

**Status is a projection.** `sessions.status` always equals the `to` of the latest `session.status_changed` event. Both are written in the same transaction.

## 4. Event catalog

**Fenced** = written by the lease-holding Worker; the append includes the fence. **Unfenced** = written by the API, the platform, or another session's Worker; serialized by the `sessions` row lock.

| Type | Payload (key fields) | Who writes | Fenced | Status effect |
|---|---|---|---|---|
| `session.created` | agent_id, agent config snapshot (incl. Budget), parent_id, depth, trigger | API / parent Worker | no | → `runnable` |
| `session.status_changed` | from, to, reason (`end_turn`, `awaiting_approval`, `budget`, `error`, `interrupted`, `crash_loop`, `user_retry`, …) | Worker or API | Worker: yes; API: no | the projection itself |
| `user.message` | message (neutral), upload refs, client_msg_id, skill? (`{skill_id, body}` from `/skill-name`, [agents-skills.md](agents-skills.md) D26) | API | no | `awaiting_user`/`completed`/`failed` → `runnable`; `running` → Steering, no status change; clears `background` |
| `user.interrupt` | reason | API | no | sets `cancel_requested` |
| `session.backgrounded` | — (from `/background`) | API | no | sets `background = true`; wall-clock Budget starts at 0 |
| `session.config_changed` | agent_id?, snapshot?, model?, mode?, budget? (from `/agent`, `/model`, `/mode`, `/budget`; [agents-skills.md](agents-skills.md) §7) | API | no | — ; `Fold` applies in order, latest wins; `agent_id` also updates `sessions.agent_id` |
| `session.renamed` | title, by (`auto` / `user`) | API (`user`) or Worker (`auto`) | Worker: yes; API: no | — ; sets `sessions.title`; `auto` is skipped once a `user` rename exists |
| `turn.started` | turn_id, model, provider, input_through_seq, app_version, tools_hash (full tool list stored as a blob for replay) | Worker | yes | — (intent marker for the LLM call) |
| `llm.response` | turn_id, message (text/thinking/tool_use blocks), stop_reason, usage | Worker | yes | — |
| `turn.interrupted` | turn_id, reason (`user_interrupt`, `worker_lost`), partial text (optional) | Worker | yes | — |
| `tool.call.requested` | tool_call_id, tool, args (or blob_ref) | Worker | yes | — |
| `approval.requested` | approval_id, kind (`tool`, `budget`, `sandbox`), tool_call_id (kind=tool; one ask-verdict call at a time for a stepped batch, approver.md §5.8), reason (optional, e.g. `jev_unavailable`/`jev_quota_exhausted`), approver trace (rule/Jev + confidence) | Worker | yes | → `awaiting_approval` |
| `approval.resolved` | approval_id, decision (`allow`/`deny`), scope (`once`/`project`/`everywhere`), by, via? (`app`/`email`; [notifications.md](notifications.md)) | API | no | → `runnable`; kind=tool: only once every ask call in the batch is resolved (approver.md §5.8); budget `deny` → `awaiting_user` (§5.9) |
| `tool.call.started` | tool_call_id, idempotency_key, approved_by (`rule:<id>`/`jev`/`user`/`mode`), approver trace | Worker | yes | — (intent marker; the tool runs only after this commits) |
| `tool.call.completed` | tool_call_id, result (neutral or blob_ref), is_error, denied, duration | Worker | yes | — |
| `tool.call.interrupted` | tool_call_id, reason (`worker_lost`, `user_interrupt`), note for the model | Worker | yes | — |
| `elicitation.requested` | connector, schema, request_state | Worker | yes | → `awaiting_user` |
| `elicitation.resolved` | action (`accept`/`decline`/`cancel`), content (optional) | API | no | → `runnable` |
| `budget.exceeded` | dimension (`tokens`/`dollars`/`turns`/`wall_clock`), limit, used | Worker | yes | followed by `approval.requested(kind=budget)` |
| `sandbox.requested` / `.started` / `.stopped` / `.failed` | sandbox_id, image, reason | Worker | yes | — |
| `child.started` | child_session_id, agent, task, blocking | Worker (parent) | yes | blocking → `awaiting_children` |
| `child.completed` | child_session_id, outcome, summary, usage totals | child's Worker, into the parent's log | no (on parent); child side is fenced | last open blocking child → `runnable` |
| `context.compacted` | as [context.md](context.md) §7 | Worker | yes | — (projection only) |
| `context.cleared` | as [context.md](context.md) §7 | Worker (API for `/clear`) | Worker: yes; API: no | — (projection only) |
| `memory.written` | memory_id, path, op, version (ref only, no content; [memory.md](memory.md) §7) | Worker | yes | — |
| `artifact.saved` | artifact_id, name, version, blob_ref | Worker | yes | — (card in chat; [uploads-artifacts.md](uploads-artifacts.md)) |
| `artifact.deleted` | artifact_id | API | no | — (card shows "(deleted)"; full definition: [uploads-artifacts.md](uploads-artifacts.md)) |
| `usage.recorded` | kind, provider, model?, quantity, unit, cost_micros | Worker or API | Worker: yes; API: no | projected into `usage`; only `kind = llm` also feeds budget counters ([usage-metering.md](usage-metering.md) D7) |
| `timer.set` | wake_at, reason | Worker | yes | → `sleeping` |
| `timer.fired` | reason | Worker (on claim) | yes | `sleeping` → `running` |
| `session.error` | code, message, retryable | Worker | yes | `retryable:false` or crash loop → `failed`; `retryable:true` → `timer.set` → `sleeping` (1/5/15 min backoff, §5.20) |
| `session.completed` | final outcome (Child Sessions: result for the parent) | Worker | yes | top-level: → `awaiting_user`, clears `background`, notifies the user on their Channels if it was set; Child Session: → `completed` |

Payload rules:
- Neutral message format only (ADR 0002). Raw provider JSON may be kept only as an optional debug blob.
- Secrets never enter payloads. Provider Keys stay in the Worker; tool args are scrubbed for known secret header names.
- A denied tool call becomes `tool.call.completed{is_error: true, denied: true}`.
- Memory text never enters payloads: `memory` tool inputs and `view` results hold `{memory_id, version}` refs, resolved at projection time ([memory.md](memory.md) Decision 28).
- `turn.started`'s tool list is always stored as a blob, regardless of size; the inline payload keeps only `tools_hash`.
- `usage.recorded`: one event per non-zero token class (input, cache read, cache write 5m/1h, output, reasoning, server tools), appended in the same tx as `llm.response`; `cost_micros` comes from the price catalog at record time.

## 5. Algorithms

### 5.1 Append with fencing

```go
// Fence is nil for unfenced appends (API, platform, child.completed on the parent).
type Fence struct {
    Owner     string
    Epoch     int64
    ExpectSeq *int64 // set on Park appends: last_seq the Worker folded through
}

func (s *Store) Append(ctx context.Context, sid uuid.UUID, f *Fence, evs []NewEvent, next *StatusChange) ([]int64, error) {
    return withTx(ctx, s.db, func(tx pgx.Tx) ([]int64, error) {
        return s.appendTx(ctx, tx, sid, f, evs, next)
    })
}

func (s *Store) appendTx(ctx context.Context, tx pgx.Tx, sid uuid.UUID, f *Fence, evs []NewEvent, next *StatusChange) ([]int64, error) {
    n := len(evs)
    if next != nil { n++ }                                     // status_changed is an event too
    q := `UPDATE sessions SET last_seq = last_seq + $2, updated_at = now()`
    if f != nil { q += `, recovery_attempts = 0` }             // progress made
    q += ` WHERE id = $1`
    args := []any{sid, n}
    if f != nil {
        q += ` AND lease_owner = $3 AND lease_epoch = $4 AND lease_expires_at > now()`
        args = append(args, f.Owner, f.Epoch)
        if f.ExpectSeq != nil { q += ` AND last_seq = $5`; args = append(args, *f.ExpectSeq) }
    }
    var last int64
    var uid uuid.UUID
    if err := tx.QueryRow(ctx, q+` RETURNING last_seq, user_id`, args...).Scan(&last, &uid); err != nil {
        if errors.Is(err, pgx.ErrNoRows) {
            if f != nil && f.ExpectSeq != nil && s.fenceHolds(ctx, tx, sid, f) { return nil, ErrStale } // new events: refold
            return nil, ErrLeaseLost                           // zombie fenced out
        }
        return nil, err
    }
    if next != nil { evs = append(evs, next.Event()) }         // session.status_changed{from,to,reason}
    seqs := make([]int64, len(evs))
    for i, e := range evs {
        seqs[i] = last - int64(len(evs)-1-i)
        insertEvent(tx, sid, seqs[i], e, f)                    // PK (session_id, seq) guards against bugs
        project(tx, sid, seqs[i], e)                           // usage, budget counters, cancel_requested, …
    }
    if next != nil {
        applyStatus(tx, sid, next)                             // status, ready_at, wake_at; parking clears lease_owner/expires_at
        if next.To == "runnable" { tx.Exec(ctx, `SELECT pg_notify('jf_runnable', $1)`, sid.String()) }
        tx.Exec(ctx, `SELECT pg_notify('jf_activity', $1)`, fmt.Sprintf("%s:%s", uid, sid)) // Activity Stream hint (streaming.md)
    }
    tx.Exec(ctx, `SELECT pg_notify('jf_events', $1)`, fmt.Sprintf("%s:%d", sid, last)) // SSE hint
    return seqs, nil
}
```

- The row lock taken by the `UPDATE` serializes the API and the Worker for one session. Any error rolls the tx back, including the `last_seq` bump, so no gap appears.
- `ExpectSeq` is set only on appends that park the session (`awaiting_user`, `awaiting_approval`, `awaiting_children`, `sleeping`, `completed`). If an unfenced event (e.g. Steering) arrived after the fold, the park fails with `ErrStale`, and the Worker refolds instead of parking over an unread message.
- API appends that resume a waiting session pass `next = {To: "runnable"}` in the same tx.

### 5.2 Claim, lease, heartbeat

```go
const claimSQL = `
UPDATE sessions SET status = 'running', lease_owner = $1, lease_epoch = lease_epoch + 1,
       lease_expires_at = now() + interval '30 seconds',
       recovery_attempts = recovery_attempts + 1
WHERE id = (
  SELECT id FROM sessions
  WHERE  status = 'runnable'
     OR (status = 'sleeping' AND wake_at <= now())
     OR (status = 'running'  AND lease_expires_at < now())    -- rescue a dead Worker
  ORDER BY ready_at NULLS LAST
  FOR UPDATE SKIP LOCKED LIMIT 1)
RETURNING id, lease_epoch, recovery_attempts`

const heartbeatSQL = `
UPDATE sessions SET lease_expires_at = now() + interval '30 seconds'
WHERE id = $1 AND lease_owner = $2 AND lease_epoch = $3 AND lease_expires_at > now()
RETURNING cancel_requested`

func (w *Worker) heartbeat(ctx context.Context, sid uuid.UUID, f Fence, cancel context.CancelFunc) {
    t := time.NewTicker(10 * time.Second); defer t.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-t.C:
            var cancelRequested bool
            err := w.db.QueryRow(ctx, heartbeatSQL, sid, f.Owner, f.Epoch).Scan(&cancelRequested)
            if errors.Is(err, pgx.ErrNoRows) { w.lost.Store(sid, true); cancel(); return } // lease lost
            if cancelRequested { cancel() }                    // Interrupt fallback (≤10 s)
        }
    }
}
```

- The claim tx is the only time a row lock is held. After it, the Lease is logical (columns). Never hold a row lock or an open tx across an LLM or tool call.
- The claim tx also bumps `last_seq` and inserts `session.status_changed{to: running, reason: claimed}`, so status and event stay together. This insert does not reset `recovery_attempts`.
- All times use DB `now()`. Worker clocks are never compared with lease columns.

### 5.3 Worker loop

```go
func (w *Worker) Run(ctx context.Context) {
    wake := w.listen(ctx, "jf_runnable", "jf_cancel") // dedicated non-pooled conn; on reconnect: re-LISTEN, then poll
    for {
        for w.sem.TryAcquire() {                        // per-process cap, e.g. 50 sessions
            sid, epoch, attempts, ok := w.claim(ctx)
            if !ok { w.sem.Release(); break }
            go w.drive(ctx, sid, Fence{Owner: w.id, Epoch: epoch}, attempts)
        }
        select { case <-wake: case <-time.After(3 * time.Second): case <-ctx.Done(): return }
    }
}

func (w *Worker) drive(parent context.Context, sid uuid.UUID, f Fence, attempts int) {
    defer w.sem.Release()
    if attempts > 5 { w.failCrashLoop(parent, sid, f); return }   // §5.6
    ctx, cancel := context.WithCancel(parent)
    defer cancel()
    w.cancels.Store(sid, cancel); defer w.cancels.Delete(sid)
    go w.heartbeat(ctx, sid, f, cancel)

    for {
        evs := w.store.Load(ctx, sid)                  // all events, upcast to latest schema
        st := Fold(evs)                                 // pure
        step := Decide(st)                              // pure
        if step.Kind.Parks() { f.ExpectSeq = &st.LastSeq } else { f.ExpectSeq = nil }
        err := w.exec(ctx, sid, f, step)                // appends via Store.Append(…, &f, …)
        switch {
        case errors.Is(err, ErrStale):                  continue        // new events arrived; refold
        case errors.Is(err, ErrLeaseLost):              return          // someone else owns it; do nothing more
        case ctx.Err() != nil && w.lost.Load(sid):      return          // heartbeat saw lease loss
        case ctx.Err() != nil:                          w.finishInterrupt(sid, f, st); return
        case err != nil:                                w.fail(sid, f, err); return
        case step.Kind.Parks():                         return          // status set, lease cleared in same tx
        }
    }
}
```

`exec(step)`:
- `StartTurn`: budget check (§5.8); append `turn.started{input_through_seq, app_version, tools_hash}` (the full tool list is stored as a blob for replay); call the Provider with ctx, publishing deltas to `DeltaBus`; append `llm.response` + `usage.recorded` in one tx; budget check after the response.
- `RequestTool`: append `tool.call.requested`; run the Approver; append `approval.requested` + park, or continue.
- `StartTool`: budget check; append `tool.call.started{idempotency_key, approved_by, approver trace}`; **only if that append committed**, call the tool with ctx; append `tool.call.completed`.
- `MarkInterrupted`: append the `turn.interrupted` / `tool.call.interrupted` events from §5.5.
- `Park(status)`, `Complete`: append the status change (with `ExpectSeq`).

### 5.4 Fold and Decide

```go
type State struct {
    LastSeq         int64
    Status          string
    Budget          BudgetLimits          // from session.created snapshot
    Used            BudgetUsed            // tokens, cost, turns, wall clock
    InputThroughSeq int64                 // from the latest turn.started
    OpenTurn        *Turn                 // turn.started without llm.response or turn.interrupted
    PendingToolUse  []ToolUse             // tool_use blocks in the last llm.response not yet requested
    Requested       map[string]ToolCall   // requested, with approval state
    OpenTool        *ToolCall             // tool.call.started without completed/interrupted
    PendingUserSeqs []int64               // user.message seq > InputThroughSeq
    OpenApproval    *Approval
    OpenChildren    map[uuid.UUID]bool    // blocking child.started without child.completed
    CancelRequested bool
    IsChild         bool
}

func Fold(evs []Event) State              // pure; no I/O, no clock
func Decide(s State) Step                 // pure; returns one Step
```

`Decide` rules, first match wins:

| State | Step |
|---|---|
| `OpenTurn != nil` | `MarkInterrupted` (turn, `worker_lost`), then the loop continues and the turn is re-run |
| `OpenTool != nil` | `MarkInterrupted` (tool, `worker_lost`) |
| `OpenApproval != nil` | `Park(awaiting_approval)` |
| `len(OpenChildren) > 0` | `Park(awaiting_children)` |
| requested tool, approval allowed (or not needed), not started | `StartTool` |
| requested tool, approval denied | append `tool.call.completed{is_error, denied}` |
| `PendingToolUse` not empty | `RequestTool` (next, in order) |
| all tool results in, or `PendingUserSeqs` not empty | `StartTurn` (includes pending Steering) |
| last `llm.response` has `end_turn` and no pending user messages | `Complete` (top-level → `awaiting_user`; Child Session → `completed` + `child.completed` on the parent, §5.9) |

- `Decide` is golden-tested with a fake Provider: fixture event list → expected Step.
- Never replay code. Harness changes between crashes are safe because only data is folded.
- Upcasters: `upcast(type, version, payload) → latest payload`, pure, golden-tested per version. Stored rows are never rewritten. Only add optional fields; a rename is a new type plus an upcaster. The neutral message format carries its own version, `msg_v`, inside every payload that embeds a `msg.Message` (`llm.response`, `user.message`, `tool.call.completed`) (ADR 0002). Adding a new `Part` kind is additive and does not bump `msg_v`; a shape change to an existing kind does. A Worker that reads an unknown `msg_v` or `Part` kind releases the Lease without running `Decide`, same as an unknown `schema_version` (§5.19).
- Rebuild test: `Fold(all events)` must equal the `sessions` row's projected columns (status, last_seq, counters).

### 5.5 Recovery

There is no separate recovery path. A rescued session goes `claim → Fold → Decide`, and `Decide` handles the tail:

| Tail state | Action |
|---|---|
| `turn.started`, no `llm.response` | Append `turn.interrupted{reason: worker_lost}` plus `usage.recorded` if the Provider reported usage before the crash. Then re-run the turn (new `turn.started`). |
| `tool.call.started`, no completed/interrupted | Append `tool.call.interrupted{reason: worker_lost, note: "outcome unknown; check before retrying"}`. It is the tool result the model sees. **Never** re-run the tool. |
| `tool.call.requested`, approval allowed, not started | Start it (no side effect happened yet). |
| `approval.requested` unresolved | Park `awaiting_approval`. |
| `llm.response` with tool_use blocks not all requested | Request the remaining calls in order. |
| All tool results in | Inject pending Steering, start the next turn. |

- Guarantees: at-most-once start for tools, at-least-once for LLM calls.
- Tool idempotency key = `"{session_id}:{seq of tool.call.started}"`. Pass it to built-in tools and to MCP as metadata (best effort; MCP has no standard key).

### 5.6 Crash-loop limit

- `recovery_attempts` is incremented by every claim and reset to 0 by every successful fenced append.
- If a claim returns `recovery_attempts > 5` (the previous 5 claims made no successful fenced append), the Worker does not run `Decide`. It appends `session.error{code: crash_loop, retryable: false}` + status `failed` (reason `crash_loop`) and notifies the user on their Channels: "This chat hit an error. Retry?"

### 5.7 Waiting states

| Status | Entered by | Left by | Holds Worker / Sandbox |
|---|---|---|---|
| `runnable` | any resume event | claim | no |
| `running` | claim | park, terminal status, lease loss | yes / maybe |
| `awaiting_approval` | `approval.requested` (tool, budget, sandbox) | `approval.resolved` | no (Sandbox stopped when idle) |
| `awaiting_user` | `session.completed` (end of turn), `elicitation.requested`, Interrupt | `user.message`, `elicitation.resolved` | no |
| `awaiting_children` | blocking `child.started` | last open blocking `child.completed` | no |
| `sleeping` | `timer.set` | claim sees `wake_at <= now()` → `timer.fired` | no |
| `completed` / `failed` | terminal events | `user.message` reopens | no |

Resume is always one unfenced tx: event + `status='runnable'` + `NOTIFY jf_runnable`. Approvals arriving by email or Telegram use the same POST path.

Wakeups:
- `NOTIFY jf_runnable, '<session_id>'` on every tx that makes a session runnable. It is only a hint.
- Workers LISTEN on a dedicated non-pooled connection (PgBouncer transaction pooling cannot LISTEN).
- Workers also poll every 3 s and immediately after any (re)connect, because a disconnected listener misses notifications.

### 5.8 Steering and Interrupt

**Steering** (data):
1. API appends `user.message` while `status='running'`. No status change.
2. At each tool-result boundary the Worker refolds (`events > cursor`), sees `PendingUserSeqs`, and includes them in the next turn.
3. `turn.started.input_through_seq` records the last user seq the model saw.
4. The log keeps wall-clock order (a message may sit between `tool.call.started` and `completed`); the context builder places it after the results. Tool results and steering text may reach the Provider as two consecutive user messages; adapters do not merge them.

**Interrupt** (signal):
1. API, in one tx: append `user.interrupt`, set `cancel_requested=true`, `NOTIFY jf_cancel, '<session_id>'`.
2. Each Worker keeps `map[sessionID]context.CancelFunc`. On `jf_cancel` for a session it holds, it calls cancel. Fallback: the heartbeat returns `cancel_requested` (≤10 s).
3. In-flight LLM and tool calls see `ctx.Done()`. Sandbox exec kills the process in the container. Remote MCP calls send the MCP cancellation notification.
4. `finishInterrupt` (fenced, on a fresh ctx with a short timeout): append `turn.interrupted{user_interrupt}` or `tool.call.interrupted{user_interrupt}`, clear `cancel_requested`, status `awaiting_user` (reason `interrupted`).

### 5.9 Budget checks

1. Limits come from the Agent config snapshot in `session.created`, or the latest `session.config_changed` snapshot (`/agent`): tokens, dollars, turns, wall clock. `PUT /api/sessions/{id}/budget` writes a `budget` there, which replaces all limits and clears earlier allows. A Child Session's tokens and dollars are also added to its parent's counters on `child.completed` (§5.10).
2. Scope: tokens and dollars accumulate over the whole session. Turns reset with each `user.message`: `Fold` counts `turn.started` events since the last `user.message`; no reset event or extra counter. Wall clock counts only `running` time — waiting (`awaiting_*`) and `sleeping` never count — and applies only to Background Sessions, Child Sessions and System Sessions; a foreground chat session never hits a wall-clock limit. A session becomes a Background Session only via `/background`, which starts its clock at 0; the user's next message returns it to foreground and ends the clock; `/background` again restarts the clock at 0. On/off state: `session.backgrounded` event + `sessions.background` column (same tx). It clears on the next `user.message` or when the agent finishes (`session.completed`); finishing also notifies the user on their Channels. After that the session is a normal foreground chat.
3. Check **before** each `StartTurn` and `StartTool`, using the projected counters on `sessions` (`tokens_used`, `cost_micros`, `turns`).
4. Check **after** each `llm.response` (tokens and dollars now include this turn).
5. On exceed, one fenced tx: `budget.exceeded{dimension, limit, used}` + `approval.requested{kind: budget}` + status `awaiting_approval` (reason `budget`). The Worker returns; the lease is cleared.
6. Resume is the normal `approval.resolved` path: **allow** raises the exceeded dimension by one more increment equal to the original limit (e.g. 50k → 100k) and resumes; hitting the new ceiling asks again. **deny** leaves the session `awaiting_user` instead of `runnable`. The User answers with `POST /api/sessions/{id}/approvals/{approval_id}` `{decision}`.

### 5.10 Delegation parking

0. Before this, the Worker enforces depth ≤ 2 and ≤ 5 running children per session ([agents-skills.md](agents-skills.md) D7–D8).
1. Parent Worker appends `child.started{child_session_id, blocking}` and creates the child session (`session.created`, `parent_id`, `depth+1`, status `runnable`) in the same tx.
2. Blocking: the same tx parks the parent (`awaiting_children`), clears its lease. The parent holds no Worker while it waits.
3. The child runs like any session.
4. Child's final tx (the one that sets `completed` or `failed`):

```go
func (s *Store) FinishChild(ctx context.Context, child, parent uuid.UUID, f Fence, final []NewEvent, res ChildResult) error {
    return withTx(ctx, s.db, func(tx pgx.Tx) error {
        // Lock order: parent, then child. Always. Prevents deadlock with any other two-row tx.
        tx.Exec(ctx, `SELECT 1 FROM sessions WHERE id = $1 FOR UPDATE`, parent)
        if _, err := s.appendTx(ctx, tx, child, &f, final, &StatusChange{To: res.Status}); err != nil { return err }
        var next *StatusChange
        if s.lastOpenBlockingChild(ctx, tx, parent, child) { next = &StatusChange{To: "runnable"} }
        _, err := s.appendTx(ctx, tx, parent, nil, []NewEvent{childCompleted(child, res)}, next)
        return err
    })
}
```

- `lastOpenBlockingChild`: true if no other blocking `child.started` in the parent's log lacks a `child.completed`, and the parent is `awaiting_children`.
- The same tx adds the child's token and dollar totals to the parent's `tokens_used` and `cost_micros` (Budget roll-up, [agents-skills.md](agents-skills.md) D11). The `usage` table keeps the child's rows on the child only.
- If the child's fenced append fails, the whole tx rolls back; nothing reaches the parent.
- **Non-blocking** `child.started` skips step 2 (no park). `child.completed` still reaches the parent through the same `FinishChild` tx, but `lastOpenBlockingChild` is false — non-blocking children are never tracked in `OpenChildren` — so `next` stays nil: no status change, no `NOTIFY`. The parent does not wake for it; it is consumed whenever the parent's `Fold` next runs, whatever triggers that turn.

### 5.11 Token deltas (DeltaBus)

```go
type DeltaKind uint8 // 0 = text (zero value keeps existing callers correct); also thinking, tool_start, tool_args

type Delta struct {
    SessionID uuid.UUID
    TurnID    string
    Idx       int       // neutral part index within the assistant message
    Kind      DeltaKind
    Text      string    // text / thinking / partial tool-args JSON fragment
    CallID    string    // Kind=tool_start|tool_args: our ToolUse.ID, so parallel calls stay separate
    Name      string    // Kind=tool_start: tool name, for the card title
}

type DeltaBus interface {
    Publish(ctx context.Context, d Delta) error
    Subscribe(ctx context.Context, sid uuid.UUID) (<-chan Delta, func())
}
```

- Postgres implementation: `pg_notify('jf_stream', json(Delta))`. Coalesce text per turn every 50 ms. Keep each payload under 8 KB; split if needed.
- No usage delta: usage is authoritative only on the final `llm.response`/`usage.recorded`; nothing durable reads a delta.
- Deltas are never written to `events`. The final `llm.response` is the durable record.
- Export `pg_notification_queue_usage()` as a metric. Swap the implementation to Redis or NATS if it shows pressure.

### 5.12 SSE live + replay

```
GET /api/sessions/{id}/events    (Last-Event-ID: 41, or ?after=41)
1. authz via TenantScope
2. subscribe to the in-process hub for {id} (fed by LISTEN jf_events + DeltaBus on the API node)
3. SELECT * FROM events WHERE session_id=$1 AND seq > 41 ORDER BY seq   → send each with "id: <seq>"
4. drain the hub:
     jf_events hint → SELECT seq > lastSent (never use the hint payload as data)
     delta          → send as "event: delta" with NO id
5. ": ping" comment every 15 s; "retry: 2000"
```

- Subscribe (2) before the replay query (3). Dedupe by `seq <= lastSent`.
- Deltas carry no `id`, so a reconnect resumes from the last durable seq.
- On reconnect the client drops any partial bubble for a turn with no `llm.response` and waits for new deltas or the final event.
- Missed hints are harmless: the next hint or the ping timer re-queries `seq > lastSent`.
- Child Sessions: the parent's stream shows `child.*` events; the UI opens a separate stream per expanded child card.
- Full stream rules (header beats `?after`, bad ids → replay from 0, backpressure, child stream end with 204, UI serializer, per-user Activity Stream): [streaming.md](streaming.md).

### 5.13 Hard delete

```go
func (s *Store) HardDelete(ctx context.Context, ts TenantScope, root uuid.UUID) error {
    ids := []uuid.UUID{}
    err := withTx(ctx, s.db, func(tx pgx.Tx) error {
        ids = s.sessionTree(ctx, tx, ts, root)                           // recursive on parent_id, tenant-scoped
        tx.Exec(ctx, `
            INSERT INTO usage (workspace_id, project_id, user_id, kind, unit, provider, model, quantity, cost_micros, created_at)
            SELECT workspace_id, project_id, user_id, kind, unit, provider, model, SUM(quantity), SUM(cost_micros),
                   date_trunc('month', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
            FROM usage WHERE session_id = ANY($1)
            GROUP BY workspace_id, project_id, user_id, kind, unit, provider, model,
                     date_trunc('month', created_at AT TIME ZONE 'UTC')`, ids)       // collapse into monthly totals; session_id/seq NULL by default
        tx.Exec(ctx, `DELETE FROM usage WHERE session_id = ANY($1)`, ids)             // drop the per-event rows just collapsed
        tx.Exec(ctx, `INSERT INTO deletions (id, workspace_id, session_ids) VALUES ($1,$2,$3)`, uuid.New(), ts.WorkspaceID, ids)
        _, err := tx.Exec(ctx, `DELETE FROM sessions WHERE id = $1`, root) // CASCADE: events, child sessions
        return err
    })
    if err == nil { s.jobs.Enqueue("cleanup_deletion") }
    return err
}
```

- `usage` rows for the deleted tree collapse into one total row per (workspace, project, user, kind, unit, provider, model, month), dated the 1st of that month UTC, so monthly Quotas stay right ([usage-metering.md](usage-metering.md) D6); the `bigserial id` keeps each total addressable with `session_id`/`seq` NULL.
- Async cleanup job (retryable, driven by `deletions` rows with `completed_at IS NULL`): delete blobs by prefix `ws/{w}/sess/{s}/` for each id, the Sandbox and Workspace Volume, and Artifacts; then set `completed_at`.
- No Provider-side copies to clean up: Uploads are always sent inline (base64 from our blob, [provider-gateway.md](provider-gateway.md)), never through a Provider's Files API.
- Project File blobs live under a project-level storage location, not the session prefix, so deleting a session's blobs by `ws/{w}/sess/{s}/` prefix never removes them (`ws/{w}/proj/{p}/files/`, [uploads-artifacts.md](uploads-artifacts.md) Decision 23).
- Memories derived from the session stay; `source_session_id` becomes NULL ([memory.md](memory.md)).
- UI privacy note on the delete dialog: "Deleting a chat removes all content; usage totals are kept for billing."

### 5.14 Export

- Stream `SELECT … FROM events WHERE session_id=$1 ORDER BY seq` as JSON Lines, for the session and each Child Session.
- Package the export as one ZIP archive containing the JSON Lines log(s) and every referenced blob.

### 5.15 Tenancy

- `workspace_id` on `sessions` and `events`; `project_id` and `user_id` on `sessions`.
- Every repository method takes a `TenantScope{WorkspaceID, UserID}` and filters by it. A session in another workspace is "not found".
- Workers are the only cross-tenant readers, and only through the claim query; after claim they act on that one session.
- Two types: the API-facing `Repo`, where every method takes `TenantScope` (enforced by a test), and the Worker's `Store` (claim, heartbeat, `Append`, `Load` by session id), which is exempt because the claim is its authorization.

### 5.16 Retry after `failed`

- Retry resumes the *same* session from its existing log. The API appends one unfenced tx: `session.status_changed{to: runnable, reason: user_retry}`, and resets `recovery_attempts` to 0.
- It is not a synthesized `user.message`. `Decide` sees the same tail state it would after any other resume and proceeds via the normal recovery rules (§5.5).

### 5.17 Interrupt when not `running`

Extends §5.8:
- Any status other than `running`: no-op, **except** `awaiting_children`.
- `awaiting_children`: Interrupt every blocking Child Session (recursively — a child may itself be `awaiting_children`), then park the parent `awaiting_user` (reason `interrupted`).
- Interrupt on a parent always propagates to its Child Sessions, whether or not the parent itself is `running`.

### 5.18 Elicitation

Two MCP elicitation shapes:
- **New (`input_required` tool result, multi round-trip)**: append `elicitation.requested{connector, schema, request_state}` and park (`awaiting_user`, `ExpectSeq` set), releasing the Worker's Lease. `request_state` is an opaque string handed back by the Connector; stored as is, never parsed, not a secret. On `elicitation.resolved{action, content?}` (unfenced, → `runnable`), the next claim resumes the same tool call with the answer.
- **Legacy (`elicitation/create`)**: the connector call blocks synchronously; the Worker holds the tool call — and its Lease — open for up to 5 minutes. This is the one exception to "a waiting session holds no Worker" (§6): the session is never parked while blocked on a legacy elicitation. On timeout, the tool call ends `tool.call.completed{is_error: true, "needs user input; timed out"}`.
- Full protocol handling: [mcp-client.md](mcp-client.md).

### 5.19 Rolling deploys

- A Worker that claims a session and sees an event's `schema_version` higher than it knows (a newer Worker already wrote that type's newer shape) releases the Lease immediately without running `Decide`, so a compatible Worker picks it up on the next claim.
- Release writes no event: the session stays `running` with its Lease expired, so the next claim rescues it, and the release returns the claim's `recovery_attempts` increment so it never counts toward the crash-loop limit (§5.6). The releasing Worker skips that session for a minute so it doesn't re-claim it in a loop, appending a claim event each time; other Workers claim it at once.
- This is on top of, not instead of, upcasters (§5.4): upcasters handle a Worker reading an *older* payload; this handles a Worker that cannot yet read a *newer* one.

### 5.20 Retryable session errors

On a retryable Provider or tool error:
1. The agent loop retries in-process up to 4 times before treating it as session-level (no event written).
2. If all 4 fail, append `session.error{code, message, retryable: true}`, then `timer.set` to sleep.
3. Backoff: retry after 1 min; if that fails too, 5 min; then 15 min — each failed retry re-appends `session.error{retryable: true}` and re-sleeps for the next interval.
4. If the retry after the 15 min sleep still fails, the session goes `failed` and the user is notified on their Channels.

## 6. Rules and invariants

- The Event Log is append-only. The only allowed mutation is hard delete.
- Every projection change (`sessions` columns, `usage`, budget counters) happens in the same tx as its event.
- `seq` must be gapless per session and assigned only by bumping `sessions.last_seq` in the append tx. Never use a sequence or `max(seq)+1`.
- Every Worker append must include the fence (`lease_owner`, `lease_epoch`, `lease_expires_at > now()`). Zero rows updated means the lease is lost: stop, cancel ctx, write nothing more.
- A tool must never be called unless its `tool.call.started` append committed.
- A tool call must never be re-run automatically. Unknown outcomes become `tool.call.interrupted`.
- An LLM turn interrupted by a Worker crash is re-run automatically after `turn.interrupted{worker_lost}`.
- Never replay code. Recovery = `Fold` + `Decide`. `Fold` and `Decide` must stay pure (no I/O, no clock).
- Never rewrite stored events for schema changes; use upcasters.
- Never hold a row lock or open tx across an LLM call, tool call, or other network I/O.
- All lease timing uses the DB clock.
- NOTIFY is a hint only. Correctness must never depend on receiving one; always poll as a fallback.
- `session.status_changed` and `sessions.status` are always written together.
- A waiting session (`awaiting_*`, `sleeping`) must hold no Lease and no Worker.
- Park appends must carry `ExpectSeq`; a park must never skip an unread event.
- Two-row transactions lock the parent row before the child row.
- Token deltas are never persisted.
- Inline payload parts over 32 KB go to blob storage; the event holds `blob_ref` + a 2 KB preview.
- Secrets and memory content never enter the Event Log.
- Every query is tenant-scoped through `TenantScope`.
- Hard delete removes all content; `usage` collapses into one total row per (workspace, project, user, kind, unit), with `session_id` and `seq` set to NULL.
- Retry after `failed` is `session.status_changed{to: runnable, reason: user_retry}` (unfenced), resetting `recovery_attempts` to 0; it is never a synthesized `user.message`.
- Budget approval `allow` raises that dimension's limit by one more increment equal to the original limit and resumes; `deny` leaves the session `awaiting_user`.
- Wall-clock Budget counts only `running` time, and only for Background Sessions, Child Sessions and System Sessions; foreground chat never counts against it.
- Turn Budget resets with each `user.message`; token and dollar Budgets accumulate over the whole session.
- Interrupt on a session that isn't `running` is a no-op, except `awaiting_children`: it stops all children and parks the parent `awaiting_user`. Interrupt on a parent always propagates to its Child Sessions.
- Legacy MCP `elicitation/create` is the one exception to "a waiting session holds no Worker": the Worker holds the Lease up to 5 min while blocked on it.
- A Worker that sees a `schema_version` higher than it knows releases the Lease without running `Decide`, so a compatible Worker can claim it.
- Export packages the JSON Lines event log(s) and all referenced blobs into one ZIP archive.

## 7. Decisions

All accepted 2026-09-24.

1. **Fold data plus a pure `Decide`.** Never replay code. Upcasters handle schema drift.
2. **Crash mid-LLM-call:** auto re-run the turn, recording `turn.interrupted` and any known usage. Tool calls are never re-run (Interrupted Tool Call).
3. **Hard delete keeps anonymized usage totals** (`session_id` set to NULL) for Quotas and billing. Privacy note: "Deleting a chat removes all content; usage totals are kept for billing."
4. **Crash-loop limit:** 5 consecutive recoveries with no successful append → status `failed`, and notify the user ("This chat hit an error. Retry?").
5. **A blocking `delegate` parks the parent** (`awaiting_children`) and holds no Worker. `child.completed` is appended to the parent in the child's final transaction, locking parent then child.
6. **Per-session gapless `seq`** via `sessions.last_seq`, also used as the SSE id.
7. **API-side events are written straight into the log** (unfenced, row-locked). No inbox table.
8. **Lease:** 30 s TTL, heartbeat every 10 s, DB clock only.
9. **Token deltas are ephemeral** (`pg_notify` coalesced to 50 ms, <8 KB) behind a `DeltaBus` interface. Only the final `llm.response` is durable.
10. **Interrupt** via `NOTIFY jf_cancel` → `CancelFunc` map; heartbeat `cancel_requested` is the fallback (≤10 s).
11. **Payloads over 32 KB inline go to object storage** as `blob_ref` (sha256 under the session prefix) plus a 2 KB preview.
12. **No RLS for now**; enforce app-level `TenantScope`. RLS later.
13. **No partitioning** until ~100M rows.
14. **`session.status_changed` is both an event and a column**, written in the same transaction.
15. **Budget** is checked before each turn/tool (projected counters) and after `llm.response`. Exceeding it → `budget.exceeded` + `approval.requested(kind=budget)` → park.
16. **NOTIFY per delta is fine at friend scale.** Monitor `pg_notification_queue_usage()`; swap `DeltaBus` to Redis or NATS if needed.

Accepted 2026-09-26:

17. **Retry after `failed`** resumes the same session from its log via unfenced `session.status_changed{to: runnable, reason: user_retry}`, resetting `recovery_attempts` to 0. Never a synthesized `user.message`.
18. **Budget approval outcome:** `allow` raises the exceeded dimension by one more increment equal to the original limit (e.g. 50k → 100k) and resumes; hitting it again asks again. `deny` → `awaiting_user`.
19. **Wall-clock Budget** counts only `running` time, and applies only to Background Sessions, Child Sessions and System Sessions. `/background` starts the clock at 0; the next user message ends it; `/background` again restarts it at 0.
20. **Budget scopes:** turns reset per `user.message`; tokens and dollars accumulate over the whole session.
21. **Hard delete collapses `usage`** into one row per (workspace, project, user, kind, unit); keeps the `bigserial id`.
22. **Interrupt when not `running`** is a no-op, except `awaiting_children` (stops all children, parks the parent `awaiting_user`); Interrupt always propagates to Child Sessions.
23. **Elicitation:** new MCP `input_required` parks `awaiting_user` (multi round-trip via `elicitation.requested`/`.resolved`); legacy `elicitation/create` holds the Worker up to 5 min, then times out the tool call. Legacy is the one exception to "waiting holds no Worker."
24. **Non-blocking delegate:** `child.completed` is appended to the parent's log immediately but never wakes it; the parent consumes it on its next turn.
25. **`session.error{retryable:true}` backoff:** 4 in-process quick retries, then sleep/retry at 1, 5, 15 min, then `failed` + notify.
26. **Rolling deploys:** a Worker seeing a higher `schema_version` than it knows releases the Lease without deciding.
27. **Export packaging:** one ZIP with the JSON Lines log(s) and all referenced blobs.
28. **Spec-introduced mechanics accepted as specified:** `ExpectSeq` on park appends, the `sleeping` claim branch, `usage`'s `bigserial id`.
29. **Rename `compaction` → `context.compacted`** everywhere.
30. **`tool.call.started` gains `approved_by` + an approver trace**; no new event type.
31. **`turn.started` gains a tool-list hash**; the full list is stored as a blob for replay.
32. **`user.message` still reopens a `failed` session.** Retry (§5.16) is a second way, not the only one.
33. **Background state:** `session.backgrounded` event (API, unfenced) + `sessions.background` projection column. Cleared by the next `user.message` or by `session.completed` (which also notifies the user on their Channels); the session then continues as a normal foreground chat.
34. **Per-message turn count is derived by `Fold`** (`turn.started` since the last `user.message`); no reset event.

Accepted 2026-09-27 ([agents-skills.md](agents-skills.md)):

35. **`session.config_changed{agent_id?, snapshot?, model?, mode?}`** records `/agent`, `/model` and `/mode`; latest wins in `Fold`.
36. **Child Budget roll-up**: `FinishChild` adds the child's tokens and dollars to the parent's counters; turns and wall clock don't roll up.
37. **Delegation caps**: depth ≤ 2, ≤ 5 running children per session.

Accepted 2026-09-27 ([streaming.md](streaming.md)):

38. **`jf_activity` NOTIFY** (`<user_id>:<session_id>`) in the same tx as every `session.status_changed`; feeds the per-user Activity Stream.

Accepted 2026-09-27 ([usage-metering.md](usage-metering.md)):

39. **`usage` gains `provider` and `model`.**
40. **Hard-delete usage collapse keeps the month** (`created_at` = first of that month, UTC).
41. **Only `kind = llm` usage feeds Budget counters** (`tokens_used`, `cost_micros`) and the child roll-up.

Accepted 2026-09-27 ([notifications.md](notifications.md)):

42. **Notification outbox rows** are written in the same tx as `approval.requested`, `elicitation.requested`, background `session.completed`, and → `failed`; `approval.resolved` gains optional `via`.

Accepted 2026-10-03 (S5 slice grill, [streaming.md](streaming.md) D17–D21):

43. **`sessions.trigger`** (`user_message` | `memory_tidy`) mirrors `trigger` in `session.created`; a System Session is any trigger other than `user_message`.
44. **`session.renamed{title, by}`** is the only way a Title changes; `sessions.title` is its projection in the same tx. A user rename is trimmed, 1–100 chars, top-level sessions only, and always wins over `auto`.
45. **Automatic Title**: once per top-level session, the Worker makes one side call on the session's own model, in parallel with the first Turn, from the first user message only ("Write a 3–6 word title… Reply with the title only.", no thinking). It writes `usage.recorded` and `session.renamed{by: auto}`; it is not a Turn and is not shown in the chat. On failure the placeholder stays; no retry. The Fake Provider echoes the cut first message.

## 8. Edge cases

- **Zombie Worker** (paused past TTL, then resumes): its next append or heartbeat updates 0 rows → `ErrLeaseLost`; it stops. If it was mid tool call, the new holder has already recorded `tool.call.interrupted`.
- **Lease lost after `tool.call.started`, during the call**: the side effect may happen; the new holder records `tool.call.interrupted{worker_lost}`. This is the accepted window.
- **Double POST of a user message**: the `client_msg_id` unique index rejects the second; the API returns the existing seq.
- **Steering arrives while the Worker is about to park**: the park's `ExpectSeq` check fails → `ErrStale` → refold → `StartTurn` with the message.
- **Interrupt NOTIFY missed**: heartbeat returns `cancel_requested` within 10 s.
- **Listener connection drops**: re-LISTEN, then poll immediately; missed `jf_runnable` hints cost at most one poll interval.
- **SSE reconnect mid-turn**: streamed text since the last durable event is lost from view until `llm.response` arrives; the client drops the partial bubble.
- **Provider reports no usage before a crash**: `turn.interrupted` is written with no `usage.recorded`.
- **Crash between `llm.response` and requesting tools**: `Decide` requests the remaining tool_use blocks in order.
- **Child fails (crash loop)**: its final tx is the `failed` one; `child.completed{outcome: failed}` still reaches the parent.
- **Hard delete of a parent**: children are deleted by CASCADE and included in `deletions.session_ids`; their usage is anonymized too.
- **Cleanup job crashes mid-delete**: the `deletions` row stays incomplete and the job retries (deletes by prefix are idempotent).
- **Event larger than 32 KB inline**: the oversized part is a blob; the SSE and export send `blob_ref` + preview.
- **Session in another workspace**: API returns not found.
- **Retry after `failed`**: the "Retry?" action is `session.status_changed{to: runnable, reason: user_retry}` (unfenced), not a `user.message`; `recovery_attempts` resets to 0.
- **Budget approval allowed repeatedly**: each `allow` adds one more original-limit increment (50k → 100k → 150k, …); hitting the new ceiling asks again.
- **Budget approval denied**: session stays `awaiting_user`, not `runnable`.
- **Session goes to background and back**: `/background` starts the wall-clock Budget at 0 (running time only); the user's next message ends it and returns the session to foreground; a second `/background` restarts it at 0.
- **Interrupt while `awaiting_approval`**: no-op.
- **Interrupt while `sleeping`** ("Stop retrying", amended 2026-10-02): API, in one tx, appends `user.interrupt` and moves the session `sleeping` → `awaiting_user` (reason `interrupted`), clearing `wake_at`, guarded by `WHERE status = 'sleeping'`. If a Worker claimed it first, the guard matches nothing and the normal signal path (§5.8) runs instead.
- **Retry now while `sleeping` or after a stop-class `session.error`**: same as Retry after `failed`, `session.status_changed{to: runnable, reason: user_retry}`.
- **Interrupt while `awaiting_children`**: all children are stopped and the parent moves to `awaiting_user`.
- **Legacy elicitation timeout**: after 5 min blocked in the Worker, the tool call ends `tool.call.completed{is_error: true, "needs user input; timed out"}`.
- **Non-blocking child completes while the parent is `running` or `awaiting_user`**: `child.completed` lands in the parent's log with no status change or wake; the parent's next `Fold` picks it up.
- **Rolling deploy mid-claim**: a Worker whose known `schema_version` is behind an event's releases the Lease unclaimed; the claim query lets a compatible Worker take it.

## 9. Acceptance criteria

- **Chaos crash-resume test (tool)**: run a session with a fake tool that sleeps; `kill -9` the Worker after `tool.call.started` commits. Another Worker claims within ~40 s (TTL + poll). The next events are `tool.call.interrupted{worker_lost}` then `turn.started`. The fake tool's call counter is exactly 1. Record the takeover time.
- **Chaos crash-resume test (LLM)**: `kill -9` during a streaming fake-Provider call. The next events are `turn.interrupted{worker_lost}` then a new `turn.started`; the session then completes normally.
- **Zombie fencing**: `SIGSTOP` a Worker past 30 s, let another claim, `SIGCONT`. The old Worker's append returns `ErrLeaseLost`; no event with its old `lease_epoch` appears after the new claim's first event.
- **Gapless seq**: 1,000 concurrent API and Worker appends (with some forced rollbacks) on one session produce seqs 1..N with no gaps or duplicates.
- **Rebuild**: for every fixture session, `Fold(events)` equals the `sessions` row's status, `last_seq`, and counters.
- **Decide golden tests**: each tail state in §5.5 maps to the listed Step.
- **Upcasters**: a v1 fixture payload upcasts to the latest shape; stored rows are unchanged after reads.
- **Status**: every `sessions.status` change has a matching `session.status_changed` event in the same tx (checked by the rebuild test).
- **Steering**: a `user.message` appended during a tool call appears in the next turn's input; `input_through_seq` equals its seq.
- **Steering vs park race**: a message appended between fold and park results in a new turn, not `awaiting_user`.
- **Interrupt**: with NOTIFY working, an in-flight fake LLM call is cancelled within 1 s; with `jf_cancel` disabled, within 10 s. Events end with `turn.interrupted{user_interrupt}` (or `tool.call.interrupted{user_interrupt}`) and status `awaiting_user`.
- **Crash loop**: a session whose Worker is killed before its first append on every claim ends `failed` with `session.error{crash_loop}` on the 6th claim (5 claims in a row without progress), no `Decide` runs on that claim, and a notification "This chat hit an error. Retry?" is sent.
- **Waiting holds nothing**: a session in `awaiting_approval` for 1 h has `lease_owner` NULL and no goroutine in any Worker.
- **Approval resume**: `approval.resolved` sets `runnable` and a Worker claims it within one poll interval even with NOTIFY disabled.
- **Budget**: with a 1-turn limit, the second `StartTurn` produces `budget.exceeded` + `approval.requested{kind: budget}` and status `awaiting_approval`, with no second `turn.started`. A token limit crossed by one `llm.response` triggers the same before any tool starts.
- **Delegation**: a blocking delegate leaves the parent `awaiting_children` with no lease; when the child completes, the parent's log has `child.completed` and status `runnable`, in the same tx as the child's final events. Two children: parent resumes only after the second.
- **Deltas**: after a streamed turn, `events` contains one `llm.response` and no delta rows; each `jf_stream` payload is < 8 KB.
- **SSE**: reconnect with `Last-Event-ID: N` gets exactly the events with `seq > N`, in order, no duplicates; delta frames carry no `id`.
- **Blobs**: a 100 KB tool result is stored as `blob_ref` (key under the session prefix, sha256 matches) + 2 KB preview; the inline payload is ≤ 32 KB.
- **Idempotent send**: two POSTs with the same `client_msg_id` yield one `user.message`.
- **Hard delete**: after delete, no `sessions` or `events` rows remain for the tree; `usage` rows for the tree collapse into one row per (kind, unit) with workspace/project/user populated and `session_id`/`seq` NULL, totals unchanged; after the job, no objects remain under the session prefixes and `deletions.completed_at` is set. The delete dialog shows the privacy note.
- **Export**: the JSON Lines export contains every event of the session and its children in seq order, plus referenced blobs, packaged as one ZIP archive.
- **Tenancy**: every repository call without a matching `TenantScope` returns not found; a lint/test asserts all repository methods take `TenantScope`.
- **Retry**: posting Retry on a `failed` session appends `session.status_changed{to: runnable, reason: user_retry}` (no `user.message`), resets `recovery_attempts` to 0, and a Worker resumes it via the normal recovery path.
- **Budget approval outcomes**: `allow` on a 50k-token limit raises it to 100k and resumes; hitting 100k asks again; `deny` leaves the session `awaiting_user`.
- **Wall-clock Budget scope**: a foreground chat session never produces `budget.exceeded{dimension: wall_clock}`; a Background Session's clock accrues only while `running`, pauses through `awaiting_*`/`sleeping`, resets to 0 on `/background`, and stops once a user message returns it to foreground.
- **Turn Budget reset**: after a turn-limited session parks, a new `user.message` allows further turns up to the same per-message limit.
- **Interrupt propagation**: interrupting a parent with two blocking Child Sessions stops both children and parks the parent `awaiting_user`.
- **Legacy elicitation timeout**: a fake legacy connector held for 5 min ends with `tool.call.completed{is_error: true}`, and the Worker's Lease was held throughout.
- **Rolling deploy**: a Worker with a stale known `schema_version` that claims a session with a newer event releases the Lease without producing any event; the next claim by a compatible Worker succeeds.

## 10. Open gaps

None.

## 11. Research

Problem framing, engine and queue survey, comparison tables, options A–C, and all sources: [../research/event-log.md](../research/event-log.md).
