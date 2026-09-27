# Event Log, Run Queue and Leases: Research

> Background only. The build spec is [../design/event-log.md](../design/event-log.md); implementers read that, not this.

Status: research, 2026-09-24. Terms follow [`CONTEXT.md`](../../CONTEXT.md). Every factual claim has an inline primary source. Anything I could not check is marked **unverified**.

Scope: the per-Session **Event Log**, how **Workers** get and keep **Leases**, crash recovery, waiting states, Steering/Interrupt, streaming to clients, versioning, storage, and tenancy. In-session context research is in [context.md](context.md); cross-session memory research is in [memory.md](memory.md).

---

## 1. Problem and constraints

| Constraint | Source | What it forces |
|---|---|---|
| Append-only per-Session Event Log in Postgres is the **only** source of truth | [ADR 0001](../adr/0001-own-durable-execution-on-postgres.md), CONTEXT.md | Every other table (`sessions` status, usage totals, memories) is a projection that can be rebuilt from events. |
| Own queue + Leases: `SELECT … FOR UPDATE SKIP LOCKED`, heartbeats, `LISTEN/NOTIFY`; no Temporal/Restate/River | ADR 0001 | We write the claim, heartbeat, rescue, and wakeup code ourselves. |
| Interrupted Tool Calls go to the model, never retried blindly | ADR 0001, CONTEXT.md | "Intent" must be logged *before* a side effect, so a crash leaves visible evidence. |
| Neutral message format; switching models mid-session | [ADR 0002](../adr/0002-neutral-message-format.md) | Event payloads hold neutral messages, never raw provider JSON (raw can be an optional debug blob). |
| Nothing runs on the host; lazy sandboxes | [ADR 0003](../adr/0003-nothing-executes-on-the-host.md) | Sandbox lifecycle is logged as events. A waiting session holds no worker or container. |
| Approvals can park a session for days | ADR 0003 consequences, [ADR 0004](../adr/0004-layered-approver-no-implicit-rules.md) | "Waiting" is a status in the DB, not a goroutine that is still alive. |
| Child Sessions are sessions with `parent_id` | [ADR 0005](../adr/0005-agents-and-child-sessions-unified.md) | A child that finishes must append to its parent's log and wake the parent. |
| Usage metered per session (incl. Docker stats samples as events) | [ADR 0006](../adr/0006-byok-llm-platform-metered-services.md), [product.md](../product.md) | `usage.*` events plus a projected usage table. |
| SSE with `Last-Event-ID` resume; commands via POST | product.md | Event ids must be monotonic per stream, so `seq` is a natural fit. |
| Steering at the tool-result boundary; Interrupt cancels in-flight | CONTEXT.md | Two different signals: data in the log (Steering) and an out-of-band cancel (Interrupt). |
| Budgets pause the session | CONTEXT.md | `budget.exceeded` event, then the session waits on a budget Approval. |
| `memory.written` stores only references; Compaction is a projection; log never mutated | memory.md / context.md decisions | Payload rules per event type. Compaction appends a summary event; old events stay. |
| Hard delete + JSON export; multi-tenant | product.md | Deletion must be cheap per session. Every row is tenant-scoped. |
| Learning project; the user builds it in Go | product.md | Prefer a few simple tables and explicit algorithms over clever machinery. |

---

## 2. What exists now

### Durable execution engines

- **Temporal**: An Event History is "an append-only log of Events" persisted by the service ([docs](https://docs.temporal.io/workflow-execution/event)). Workflow code is *re-executed* on replay and must be deterministic: "given the same input" it must make "the same Workflow API calls in the same sequence". Changing code for in-flight executions causes non-determinism errors. The fix is Worker Versioning or patching ([docs](https://docs.temporal.io/workflow-definition)). Side effects run in Activities (`ActivityTaskScheduled` → `…Started` → `…Completed/Failed`). The server "doesn't detect failures when a Worker … crashes", so it relies on Start-To-Close and Heartbeat timeouts to force a retry ([docs](https://docs.temporal.io/encyclopedia/detecting-activity-failures)). History limits: a warning at 10,240 events, termination above 51,200 events; Continue-As-New resets the history ([docs](https://docs.temporal.io/workflow-execution/event)). Lesson: long agent sessions hit history limits. Also, the default model is "retry the activity", which is the opposite of our Interrupted Tool Call rule.
- **Restate**: records "both the operation and its result" in a journal and replays completed steps. Virtual Objects give a "single-writer guarantee: Only one handler can modify state at a time". Handlers can suspend while waiting for external events ([key concepts](https://docs.restate.dev/foundations/key-concepts), [durable execution](https://docs.restate.dev/concepts/durable_execution)). Lesson: one writer per key is exactly what our per-session Lease is.
- **DBOS**: the closest match (a library, with only Postgres as infrastructure; there is a Go SDK, [dbos-transact-golang](https://github.com/dbos-inc/dbos-transact-golang)). It does "one database write per step … plus two additional database writes per workflow". Recovery re-executes the workflow with checkpointed inputs and skips steps that have outputs. It requires deterministic workflows and idempotent steps ([architecture](https://docs.dbos.dev/architecture)). Its system tables are `workflow_status` (`executor_id`, `recovery_attempts`, `application_version`, `queue_name`), `operation_outputs` (`function_id`, "monotonically increasing ID of the step"), `notifications`, `workflow_events` and `streams` ([system tables](https://docs.dbos.dev/explanations/system-tables)). Lessons: a status row plus a step-outputs log is enough; `application_version` pins recovery to compatible code; a per-workflow monotonic step id acts as the idempotency key.
- **Inngest**: "Each step in your function is executed as a separate HTTP request". On re-invocation, completed steps return memoized results instead of running again, matched by step id ([docs](https://www.inngest.com/docs/learn/how-functions-are-executed)).
- **Cloudflare Workflows / Durable Objects**: steps "might be retried multiple times" and so should be idempotent. Step names are cache keys and must be deterministic. In-memory state is lost on hibernation. Step results must be under 1 MiB (store larger ones in R2). Use `waitForEvent` for long waits ([rules](https://developers.cloudflare.com/workflows/build/rules-of-workflows/)). Durable Objects are single-threaded, globally named objects with SQLite storage and alarms ([docs](https://developers.cloudflare.com/durable-objects/)). Lessons: put big payloads in object storage, and "timers + wait for event" are core primitives.

### Agent runtimes

- **LangGraph checkpointers**: persist "a thread's graph state as checkpoints", which enables human-in-the-loop, time travel and fault tolerance ([docs](https://docs.langchain.com/oss/python/langgraph/persistence)). The Postgres saver uses `checkpoints(thread_id, checkpoint_ns, checkpoint_id, parent_checkpoint_id, checkpoint jsonb, metadata)`, `checkpoint_blobs` (per channel/version) and `checkpoint_writes` (pending writes per task) ([source](https://raw.githubusercontent.com/langchain-ai/langgraph/main/libs/checkpoint-postgres/langgraph/checkpoint/postgres/base.py)). Lesson: this stores *snapshots* of state, not events. That is fine for resume but weaker as an audit trail and for SSE replay.
- **OpenAI Agents SDK sessions**: a small protocol, `get_items / add_items / pop_item / clear_session`, with SQLite, SQLAlchemy, Redis, Mongo, Dapr and encrypted backends ([docs](https://openai.github.io/openai-agents-python/sessions/)). It is only conversation memory, with no leases or recovery. Durability comes from the **Temporal integration** (GA 2026-03-23 per Temporal): the agent runs as a Workflow, and model calls and tools run as Activities ([Temporal docs](https://docs.temporal.io/develop/python/integrations/openai-agents)).
- **Claude Managed Agents** (beta, `managed-agents-2026-04-01`): Agent / Environment / Session / Events. User events include `user.message`, `user.interrupt`, `user.tool_confirmation` and `user.tool_result`. Agent events include `agent.message`, `agent.thinking`, `agent.tool_use` and `agent.mcp_tool_use`. There are also `session.status_running/idle/error`, `span.model_request_start/end`, and deltas `event_start/event_delta`. **Deltas "are never persisted in history."** `session.status_idle` carries `stop_reason` (`end_turn`, `requires_action` + `event_ids`). Reconnect recipe: open the stream *first*, then list history, then dedupe by event id ([events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming), [overview](https://platform.claude.com/docs/en/managed-agents/overview)). This is almost our product, so it is a strong template for naming.
- **Claude Code**: "Each message, tool use, and result is written to a plaintext JSONL file under `~/.claude/projects/`". Resume appends under the same session id; fork copies history into a new id. Pressing Esc cancels the running tool call. A typed message is queued and read "as soon as those [tool] calls finish, within the same turn" ([how it works](https://code.claude.com/docs/en/how-claude-code-works)). This matches our Interrupt and Steering definitions.

### Postgres queues (design references)

- **River** (Go): its rescuer treats a job as stuck after `RescueStuckJobsAfter` (default 1 h) and reschedules or discards it; maintenance runs on an elected leader ([docs](https://riverqueue.com/docs/maintenance-services)). Work coordinators need LISTEN/NOTIFY, and therefore session pooling or no PgBouncer. A "poll only" mode exists for transaction pooling ([docs](https://riverqueue.com/docs/pgbouncer)).
- **graphile-worker**: uses LISTEN/NOTIFY for latency ("typically under 3ms") and SKIP LOCKED for fetching; the default is 25 retries over about 3 days ([docs](https://worker.graphile.org/docs)). Crash-lock timeout details: **unverified**.
- **pgmq**: visibility-timeout semantics, where a message becomes visible again if it is not deleted or archived within `vt` ([README](https://github.com/pgmq/pgmq)). This is the same idea as a lease that expires.
- **Oban**: the Lifeline plugin rescues orphaned jobs (the plugin is deprecated in favor of `Oban.Lifeline`; defaults **unverified**, [docs](https://oban.hexdocs.pm/Oban.Plugins.Lifeline.html)).
- **Postgres NOTIFY facts** ([docs](https://www.postgresql.org/docs/current/sql-notify.html)): the payload limit is 8000 bytes. Notifications are delivered only on commit, and listeners receive them only *between* transactions. Identical notifications within one transaction are folded. Delivery is in commit order. The queue is 8 GB and NOTIFY fails when it is full. There is no replay for a listener that was disconnected.
- **PgBouncer** transaction pooling never supports `LISTEN` or session-level advisory locks ([features](https://www.pgbouncer.org/features.html)).
- **Sequences are not gapless**: `nextval` values are not reclaimed on abort, and "sequence objects cannot be used to obtain 'gapless' sequences" ([docs](https://www.postgresql.org/docs/current/functions-sequence.html)).
- **Fencing tokens** (Kleppmann): a paused lease holder can wake up after expiry. Storage must reject writes that carry a token lower than one it has already seen ([post](https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html)).

### Comparison

| System | Log unit | Recovery model | Code determinism needed | Waiting | Side-effect retry default | Relevance |
|---|---|---|---|---|---|---|
| Temporal | Event History | re-run code against history | yes (strict) | timers, signals | retry Activity | concepts; too heavy (ADR 0001) |
| Restate | journal | replay journal | yes | suspension, awakeables | retry | single-writer per key |
| DBOS | status row + step outputs (Postgres) | re-run, skip done steps | yes | recv/sleep | steps must be idempotent | **closest shape** |
| Inngest | memoized steps | re-invoke, inject results | yes (step ids) | waitForEvent, sleep | retry | step ids = keys |
| CF Workflows | step cache | re-run, cached steps | yes (names) | waitForEvent, sleep | retry | 1 MiB payload lesson |
| LangGraph | state snapshots | load last checkpoint | no | interrupt() | re-run node | snapshots vs events |
| OpenAI SDK Sessions | item list | none (use Temporal) | n/a | n/a | n/a | minimal API |
| Managed Agents | typed events + SSE | managed (internals unverified) | n/a | `requires_action` idle | unverified | **event naming, delta rule** |
| Claude Code | JSONL per session | resume/fork | no | n/a (local) | n/a | steering/interrupt UX |
| jelly-fish (proposed) | typed events (Postgres) | **fold data, then decide** | **no** | status + `wake_at` | **never** (Interrupted Tool Call) | — |

The key difference: every engine above re-executes *code* and needs determinism. An agent loop is simple enough that we can fold *data* instead. State = fold(events), and the next action = decide(state). Nothing is replayed, so harness code can change freely between crashes.

---

## 3. Core design questions

### 3.1 Event envelope

| Field | Type | Notes |
|---|---|---|
| `session_id` | uuid | Partition/ownership key. |
| `seq` | bigint | Per-session, gapless, starting at 1. Doubles as the SSE `id`. PK `(session_id, seq)`. |
| `workspace_id` | uuid | Denormalized tenant key (deletion, RLS, export). |
| `type` | text | `domain.action`, e.g. `tool.call.started`. |
| `schema_version` | smallint | Per type; used by upcasters (3.9). |
| `payload` | jsonb | Neutral format; large parts become `blob_ref`s (3.10). |
| `actor` | text | `user:<id>`, `worker:<id>`, `system`, `child:<session>`. |
| `causation_seq` | bigint null | The event this one reacts to (e.g. `tool.call.completed` → its `started`). |
| `correlation_id` | text null | `turn_id` / `tool_call_id` / `approval_id` to group spans. |
| `lease_epoch` | bigint null | Epoch of the writing worker (audit; the real fence is in the append tx). |
| `created_at` | timestamptz | Use DB `now()` (clock of record). |

Seq options:

| Option | Pros | Cons |
|---|---|---|
| Global `bigserial` only | trivial, no hot row | gaps (see sequence doc), not per-session, SSE id leaks global volume, ordering by commit ≠ by id under concurrency |
| Per-session `max(seq)+1` + `UNIQUE(session_id,seq)`, retry on conflict | no counter row | retry loops; two writers (API + worker) race |
| **Per-session counter on `sessions.last_seq`, bumped in the append tx** | gapless (the tx aborts → the bump rolls back), serializes writers per session via the row lock, the same row carries the fence | one hot row per session (fine: one session ≈ a few writes/s) |

Recommend the counter row. Keep the PK `(session_id, seq)` as a safety net.


### 3.2 Event type catalog

The draft catalog from this research is now the spec's event catalog ([../design/event-log.md](../design/event-log.md) §4). Two findings shaped it:
- Keep `tool.call.requested` (the model asked) separate from `tool.call.started` (we are about to run it). An approval sits between them.
- No separate `llm.request` event is needed. `turn.started` is the intent marker for an LLM call (it is side-effect-free, so re-running it is fine).
- Naming follows Managed Agents' `domain.action` style ([events and streaming](https://platform.claude.com/docs/en/managed-agents/events-and-streaming)).


### 3.3 Streaming LLM tokens

| Option | Pros | Cons |
|---|---|---|
| Persist every delta as an event | perfect replay of partial output | 100s of rows per turn, heavy SSE replay, and log noise nobody needs |
| Persist deltas in an UNLOGGED/TTL table | reconnect mid-turn shows partial text | a second store, cleanup job |
| **Ephemeral deltas + durable final `llm.response`** | tiny log; the pattern Managed Agents uses ("never persisted in history") | text streamed before a reconnect is lost until the final event arrives |

Transport of deltas from worker → API node (different processes in the single binary):
- `pg_notify('jf_stream', json{session_id, turn_id, idx, text})`, coalesced every ~50 ms and kept under 8000 bytes. No extra infra. But NOTIFY commits go through a global queue, and heavy use may contend (**unverified at our scale**, which is tiny).
- Later: Redis/NATS pub/sub, or the worker exposes an internal HTTP stream. Hide both behind a `DeltaBus` interface.

SSE semantics: durable events are sent with `id: <seq>`. Deltas are sent **without** `id`. Per the HTML spec, the last event ID buffer "does not get reset", so reconnect resumes from the last *durable* seq ([spec](https://html.spec.whatwg.org/multipage/server-sent-events.html)). On reconnect, the client drops any partial bubble for a turn with no `llm.response` and waits for new deltas or the final event.

### 3.4 Session state: fold vs materialized row

- A pure fold (status = latest `session.status_changed`) is correct, but the queue query "which sessions are runnable?" then has to scan events.
- **Recommend a materialized `sessions` row updated in the same tx as each append**: `status`, `last_seq`, lease columns, `wake_at`, `cancel_requested`, and budget counters. It is a projection. A `rebuild(session)` test folds the log and asserts it equals the row (a golden test).
- Snapshots are not needed at first: a session is maybe 10³–10⁴ events and folding is ms. Compaction (context.md) already bounds what goes to the model. Add `session_snapshots(session_id, seq, state jsonb)` only if profiling says so.

### 3.5 Run queue and leases

Scheduling unit = **session** (not a job per turn). Runnable = `status='runnable' AND (wake_at IS NULL OR wake_at<=now()) AND (lease_expires_at IS NULL OR lease_expires_at<now())`.

- `FOR UPDATE SKIP LOCKED` is held only for the **short claim tx**. After that, the Lease is *logical*: columns `lease_owner`, `lease_epoch`, `lease_expires_at`. We never hold a row lock or open tx for minutes. Long transactions also block NOTIFY queue cleanup ([NOTIFY docs](https://www.postgresql.org/docs/current/sql-notify.html)).
- **Fencing**: every claim does `lease_epoch = lease_epoch + 1`. Every worker append and heartbeat includes `AND lease_epoch = $mine AND lease_owner = $me`. Zero rows updated means the lease was lost: stop and cancel the ctx. This is Kleppmann's token with Postgres as the storage that checks it.
- A fence cannot stop side effects *outside* Postgres. Mitigation: append `tool.call.started` (fenced) immediately before the call. If the append fails, don't call. The window that remains (the lease is lost after the append, during the call) is exactly the case the Interrupted Tool Call rule covers.
- Timing (proposal): TTL 30 s, heartbeat every 10 s, which gives about 30 s of takeover latency after a crash. Use DB `now()` everywhere (no worker clocks).
- The heartbeat `UPDATE … RETURNING cancel_requested`. This gives a polling fallback for Interrupt with ≤10 s latency.
- Wakeups: `NOTIFY jf_runnable` (payload = session_id, and only ever a hint) on every tx that makes a session runnable. Workers LISTEN on a **dedicated non-pooled connection** (PgBouncer tx mode can't LISTEN). They also **poll** every 2–5 s and immediately after any (re)connect, because a disconnected listener misses notifications forever.
- Concurrency cap per worker (a semaphore), e.g. 50 sessions per process. Most time is spent waiting on LLM streams.
- Rescue: no separate rescuer is needed. An expired lease makes the row claimable again by the normal claim query.
- Fairness/quotas: `ORDER BY ready_at` plus a per-user running-count check in the claim query (later).

### 3.6 Crash recovery and idempotency

On claim, the worker folds the log and looks at the **tail**:

| Tail state | Action |
|---|---|
| `turn.started` with no `llm.response` | Append `turn.interrupted{worker_lost}`, then re-run the turn (LLM calls have no side effects; cost = one extra call, which gets its own `usage.recorded` if the provider reported usage). |
| `tool.call.started` with no completed/interrupted | Append `tool.call.interrupted{worker_lost, note:"outcome unknown; check before retrying"}` as the tool result. The model decides. **Never** retry. |
| `tool.call.requested`, approval resolved allow, not started | Safe to start (no side effect happened yet). |
| `approval.requested` unresolved | Release the lease, set status `awaiting_approval` (should already be set). |
| `llm.response` with tool_use blocks, some not requested | Continue requesting the remaining calls in order. |
| All tool results in | Inject pending Steering (user.message seq > `input_through_seq`), then start the next turn. |

- "Exactly once" is an illusion: we get *at-most-once start* for tools (because of the intent marker) and *at-least-once* for LLM calls.
- Idempotency key per tool call = `"{session_id}:{seq of tool.call.started}"`, passed to built-in tools and to MCP as metadata. MCP has no standard idempotency key (**unverified**; I found none in the spec), so it is best effort.
- Idempotent API writes: `user.message.client_msg_id` has a unique index, so a double POST gives a single event.
- Crash loops: count `recovery_attempts` on the session (like DBOS). After N (e.g. 5) consecutive lost leases without progress, set `status=failed` + `session.error`.

### 3.7 Waiting states

| Status | Entered by | Left by | Holds worker/sandbox? |
|---|---|---|---|
| `runnable` | any wake event | claim | — |
| `running` | claim | status change / lease loss | yes / maybe |
| `awaiting_approval` | `approval.requested` (tool, budget, sandbox) | `approval.resolved` | **no** (sandbox stopped when idle) |
| `awaiting_user` | `session.completed` for a turn (end_turn), elicitation | `user.message`, `elicitation.resolved` | no |
| `awaiting_children` | `child.started` (blocking delegate) | last `child.completed` | no |
| `sleeping` | `timer.set` | claim query sees `wake_at<=now()` → `timer.fired` | no |
| `completed` / `failed` | terminal events | `user.message` reopens (chat continues) | no |

Resume is always the same: an A-type append in one tx does event + `status='runnable'` + `NOTIFY jf_runnable`. Approvals that arrive by email or Telegram use the same POST path.

### 3.8 Steering and Interrupt with a lease holder

- **Steering** is data: the API appends `user.message` while `status='running'`. The worker, at each tool-result boundary, re-reads events `> cursor` (it is cheap, and the tx it just committed makes it cheap). It includes the new user messages and records `input_through_seq` in the next `turn.started`. When the log order (steering between `tool.call.started` and `completed`) differs from the context order (after results), the context builder resolves it; the log records wall-clock truth.
- **Interrupt** is a signal: the API appends `user.interrupt`, sets `cancel_requested=true`, and runs `NOTIFY jf_cancel, '<session_id>'`. Each worker keeps `map[sessionID]context.CancelFunc`. On notify, or on a heartbeat that returns `cancel_requested`, it calls cancel. In-flight LLM or tool calls see `ctx.Done()`. The worker then appends `turn.interrupted` / `tool.call.interrupted{user_interrupt}`, clears the flag, and goes to `awaiting_user`.
- Sandbox exec must honor ctx (kill the process in the container). Remote MCP calls send the MCP cancellation notification (behavior **unverified** per server).

### 3.9 Versioning

- Because we **fold data, not replay code**, a harness upgrade never breaks in-flight sessions the way it does in Temporal. The fold must understand old *payloads*, and that is all.
- `schema_version` per event type. Upcasters run on read: `upcast(type, v, payload) → latest`, and they are pure functions tested with golden fixtures. Never rewrite stored rows (the log is immutable). A lazy "rewrite in place" migration is possible, but it breaks immutability, so skip it.
- Rules: only add optional fields; renaming a type means a new type + an upcaster; the neutral message format has its own version inside the payload (ADR 0002).
- Rolling deploys: an old worker may read events from a newer one. Either always deploy readers first, or have the worker refuse (release the lease) when `schema_version > known`. Record `app_version` on `turn.started` for debugging (like DBOS `application_version`).

### 3.10 Storage growth, deletion, export

- Inline payloads up to about 32 KB. Postgres TOAST compresses and moves large jsonb out of line anyway (the threshold is about 2 KB; **unverified** exact default for jsonb). Anything bigger (tool outputs, file reads, screenshots) goes to object storage at `ws/{workspace}/sess/{session}/blobs/{sha256}` and is stored as `{"blob_ref":{key,size,sha256,mime}, "preview":"first 2KB"}`. This matches the Cloudflare 1 MiB lesson.
- Partitioning: not at first. Per-session hard delete = `DELETE … WHERE session_id=$1` on the PK prefix, which is cheap. Time partitioning doesn't help per-session delete. Hash partitioning by session_id is an option later.
- Retention: none by default (sessions live until the user deletes them). Usage events are copied to the `usage` projection so dashboards survive session deletion (question 9 in §6).
- Hard delete (product.md): one tx deletes the events, the session and its children (recursive on `parent_id`) and the projections. Then an async job deletes blobs by prefix, the sandbox volume and artifacts. A `deletions` row (ids + timestamp, no content) tracks progress and makes the job retryable.
- Export: stream `SELECT … ORDER BY seq` as JSON Lines plus blobs (as a zip or signed URLs). Include child sessions.
- Redaction: secrets never enter payloads (provider keys stay in the worker; tool args are scrubbed for known header names). `memory.written` holds refs only. The only mutation ever allowed is hard delete.

### 3.11 Multi-tenancy

- `workspace_id` on `sessions` and `events` (plus `project_id`, `user_id` on `sessions`). Every query goes through a repository that takes a `TenantScope`.
- RLS: Postgres RLS with `SET LOCAL app.workspace_id` per tx is solid defense in depth. It works with PgBouncer tx pooling only if the setting is per-tx (`SET LOCAL`). Workers need a bypass role, since they claim across tenants. Recommendation: app-level scoping now, RLS as a later learning exercise (question 10 in §6).

---

## 4. Options for jelly-fish

| | A. Log + materialized `sessions` row (recommended) | B. Pure log + separate `runs` queue table | C. DBOS-style checkpoints |
|---|---|---|---|
| Shape | `events` + `sessions` (status, last_seq, lease) | `events` + `runs(session_id, state, lease)` rows per activation; status derived from events | `workflow_status` + `step_outputs(session_id, step_id, output)`; loop code re-runs, skipping done steps |
| Append | one tx: bump `last_seq` (fenced) + insert + project | insert event + update run | insert step output |
| Recovery | fold data, decide next | same, but two sources to reconcile | re-execute code; needs determinism |
| Harness upgrades | free (upcasters only) | free | painful (patching/versioning) |
| SSE/audit | native (seq = id) | native | step outputs are less readable as a timeline |
| Complexity | low | medium (run lifecycle vs session lifecycle) | medium, and it re-creates the engine ADR 0001 declined |
| Learning value | high: leases, fencing, folds | high | high but engine-centric |

B's only real advantage is history per activation. We get the same by grouping events by `status_changed` spans. C fights the Interrupted Tool Call rule, because step-retry semantics are its default.

---

## 5. Recommendation

Option A. Fold data, don't replay code. One counter row per session for gapless seq and fencing. The logical lease lives in columns. NOTIFY is a hint only, with a polling fallback. Deltas are ephemeral. Schema, algorithms and pseudocode are in the spec.

---

## 6. Questions raised and outcomes

All were decided 2026-09-24 as recommended; see the spec's Decisions section for the binding text.

1. **Per-session gapless seq via a `sessions.last_seq` counter, or global bigserial?** Rec: per-session counter. It gives gapless order, SSE ids and a fence in one row.
2. **Should API-side events (user.message, approval.resolved) go straight into the log, or into an inbox table the worker copies?** Rec: straight into the log (A-type, unfenced, row-locked). The log is then the true wall-clock record, and `turn.started.input_through_seq` shows what the model saw.
3. **Fold data or replay code?** Rec: fold data + a pure `Decide`. No determinism rules, and upcasters handle schema drift.
4. **Lease TTL and heartbeat?** Rec: 30 s TTL, 10 s heartbeat, DB clock only. Measure the takeover time in the chaos test.
5. **Persist token deltas?** Rec: no. Ephemeral deltas over `pg_notify` (coalesced to 50 ms, <8 KB) behind a `DeltaBus` interface. Only the final `llm.response` is durable.
6. **How is Interrupt delivered to the lease holder?** Rec: `NOTIFY jf_cancel` → `CancelFunc` map, with the heartbeat's `cancel_requested` as the fallback (≤10 s).
7. **After a worker crash mid-LLM-call, re-run the turn automatically?** Rec: yes. It has no side effects; record `turn.interrupted` and any known usage. Tools: never (Interrupted Tool Call).
8. **Inline payload cap before moving to object storage?** Rec: 32 KB inline, larger → `blob_ref` + a 2 KB preview. Blobs are keyed by sha256 under the session prefix so delete-by-prefix works.
9. **Does usage survive hard delete of a session?** Rec: yes, keep aggregated `usage` rows with `session_id` set to NULL (needed for quotas and future billing). Outcome: accepted, with the privacy note "Deleting a chat removes all content; usage totals are kept for billing."
10. **Postgres RLS now or later?** Rec: later. Use app-level `TenantScope` now; RLS is a good learning exercise once the schema is stable.
11. **Partition `events`?** Rec: no, until the table exceeds ~100 M rows. Per-session delete via the PK prefix is cheap.
12. **Crash-loop limit?** Rec: 5 consecutive recoveries without a successful append → `failed` + notify the user.
13. **Does a blocking `delegate` park the parent (`awaiting_children`), or does the parent keep its lease while polling?** Rec: park it. `child.completed` is appended to the parent by the child's worker in the child's final tx (a cross-session tx that locks both rows in parent→child order to avoid deadlocks).
14. **Should `session.status_changed` be an event, or only a column?** Rec: an event too (UI timeline, rebuild test), written in the same tx as the column.
15. **Is a Budget check done before or after each LLM call?** Rec: before each turn/tool (using the projected counters), plus after `llm.response` for tokens. Exceeding it → `budget.exceeded` + `approval.requested(kind=budget)` → park.
16. **Is NOTIFY per delta safe under load?** Rec: fine at friend scale. Watch `pg_notification_queue_usage()`, and swap in Redis/NATS behind `DeltaBus` if needed (global-lock contention: **unverified**).

---

## 7. Sources

- Temporal events and history limits: https://docs.temporal.io/workflow-execution/event
- Temporal determinism and versioning: https://docs.temporal.io/workflow-definition
- Temporal activity failure detection: https://docs.temporal.io/encyclopedia/detecting-activity-failures
- Temporal + OpenAI Agents SDK: https://docs.temporal.io/develop/python/integrations/openai-agents
- Restate key concepts: https://docs.restate.dev/foundations/key-concepts ; https://docs.restate.dev/concepts/durable_execution
- DBOS architecture: https://docs.dbos.dev/architecture ; system tables: https://docs.dbos.dev/explanations/system-tables ; Go SDK: https://github.com/dbos-inc/dbos-transact-golang
- Inngest execution model: https://www.inngest.com/docs/learn/how-functions-are-executed
- Cloudflare Workflows rules: https://developers.cloudflare.com/workflows/build/rules-of-workflows/ ; Durable Objects: https://developers.cloudflare.com/durable-objects/
- LangGraph persistence: https://docs.langchain.com/oss/python/langgraph/persistence ; Postgres saver source: https://raw.githubusercontent.com/langchain-ai/langgraph/main/libs/checkpoint-postgres/langgraph/checkpoint/postgres/base.py
- OpenAI Agents SDK sessions: https://openai.github.io/openai-agents-python/sessions/
- Claude Managed Agents: https://platform.claude.com/docs/en/managed-agents/overview ; events and streaming: https://platform.claude.com/docs/en/managed-agents/events-and-streaming
- Claude Code sessions, steering, interrupt: https://code.claude.com/docs/en/how-claude-code-works
- River maintenance: https://riverqueue.com/docs/maintenance-services ; River PgBouncer: https://riverqueue.com/docs/pgbouncer
- graphile-worker: https://worker.graphile.org/docs
- pgmq: https://github.com/pgmq/pgmq
- Oban Lifeline: https://oban.hexdocs.pm/Oban.Plugins.Lifeline.html
- Postgres NOTIFY: https://www.postgresql.org/docs/current/sql-notify.html
- Postgres sequences (gaps): https://www.postgresql.org/docs/current/functions-sequence.html
- PgBouncer features: https://www.pgbouncer.org/features.html
- Kleppmann, fencing tokens: https://martin.kleppmann.com/2016/02/08/how-to-do-distributed-locking.html
- HTML SSE spec (Last-Event-ID): https://html.spec.whatwg.org/multipage/server-sent-events.html

