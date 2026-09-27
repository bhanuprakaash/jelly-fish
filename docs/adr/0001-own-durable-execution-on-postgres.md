# Own durable execution on a Postgres event log

We need sessions to survive a closed laptop and a crashed worker. Each session's state lives in an append-only **Event Log** in Postgres. Workers claim a **Lease** on a session (`SELECT … FOR UPDATE SKIP LOCKED`, with heartbeats). `LISTEN/NOTIFY` wakes workers when something changes. After a crash, a new worker replays the log and continues from the last completed step. An **Interrupted Tool Call** is recorded as such and handed to the model; it is never retried blindly, because tool calls have side effects.

## Considered Options

- Temporal / Restate: rejected. Heavy to operate, the framework dictates the shape of the agent loop, and it hides the part this project exists to learn.
- River (Go Postgres queue): rejected for the same learning reason. The queue and leases are small enough to own.
