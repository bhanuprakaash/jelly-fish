# Jelly-fish: Product Scope

A multi-tenant platform where anyone, not just developers, can chat with any LLM using their own keys (BYOK). Every chat is a durable agent **Session** that keeps running on the server after the user closes the laptop. Sessions can call tools, connect to MCP **Connectors**, run code in an isolated **Sandbox**, delegate to other **Agents**, and pause for human **Approvals**.

- Goal: a learning project (deep Go, agent internals, market trends) that friends can use.
- Stack: a Go backend (single binary with `api` and `worker` roles), Postgres, and a React/TypeScript/Tailwind PWA. A Go CLI comes later.
- Terms: see [`CONTEXT.md`](../CONTEXT.md). Decisions: see [`docs/adr/`](./adr/).

## Base version

### Core runtime
- Durable sessions on a Postgres **Event Log**. We write our own queue and **Leases** (`SKIP LOCKED`, heartbeats, `LISTEN/NOTIFY`). ([ADR 0001](./adr/0001-own-durable-execution-on-postgres.md))
- Crash resume: a new worker replays the log. **Interrupted Tool Calls** are handed to the model and never retried blindly.
- Sessions run in the foreground or background, entered with `/background`. Each session has a **Trigger** (only "user message" for now).
- **Steering**: a message sent mid-run is injected at the next tool-result boundary. **Interrupt** cancels the in-flight call.
- **Child Sessions**: the `delegate(agent, task)` tool, nesting with a depth cap. ([ADR 0005](./adr/0005-agents-and-child-sessions-unified.md))
- **Agents**: saved configs (instructions, model, tools, connectors, skills, permission mode, budget). "General" is the default.
- **Skills**: reusable instruction packs, loaded on demand.
- Memory: **Compaction** within a session, plus persistent **Project Memory**.
- **Budgets** per session: tokens and $ accumulate over the session; turns reset each user message; wall-clock (running time only) applies to Background, Child and System Sessions. When one is hit, the session pauses and asks.

### Providers
- A `Provider` interface over a neutral message format. Switching models mid-session is allowed. ([ADR 0002](./adr/0002-neutral-message-format.md))
- Adapters: Anthropic, OpenAI, Gemini.

### Tools and MCP
- An MCP client using the official Go SDK.
- Remote connectors only in the base version, over Streamable HTTP, with an OAuth "Connect" flow plus custom URL and headers.
- Local (stdio) connectors are later roadmap — they will run only inside the Sandbox, never on the host.
- MCP tools plus elicitation (which reuses the approval UI).
- A small catalog of popular connectors, plus "add custom".
- Built-in tools: web search, web fetch, and shell/files (sandbox only).
- Base version: web search and web fetch are each Provider's built-in tools, on the user's key ([provider-gateway.md](./design/provider-gateway.md) D6a). Later: our own search as a **Platform Service** behind a `SearchProvider` interface (Brave, Tavily; users may bring their own key).

### Sandbox
- A `Sandbox` interface (create, exec, stream, files, stop, resume). Docker implementation first. ([ADR 0003](./adr/0003-nothing-executes-on-the-host.md))
- Nothing ever runs on the host.
- Created only when needed. It is attached by the project default, the agent's request (behind an approval), or `/remote`.
- One default image (Python, Node, Go, git, common CLIs).
- Stopped when idle. The **Workspace Volume** persists and is deleted after 7 days idle.
- CPU, memory and wall-clock caps. Open network egress.

### Permissions
- **Permission Modes**: ask / auto (default) / full-auto.
- Free inside the sandbox. External tools are gated, using MCP `readOnlyHint`/`destructiveHint` as defaults for catalog Connectors only (custom-URL Connector hints are ignored: ask in ask mode, rules then Jev in auto).
- **Approver** chain: **Approval Rules**, then **Jev** (platform-provided, with a confidence threshold; unsure, unavailable, or slow falls through to the user). Users see "auto mode", never the name "Jev". ([ADR 0004](./adr/0004-layered-approver-no-implicit-rules.md), [approver.md](./design/approver.md))
- A stepped approval card walks through each call needing a decision (approve once / this project / everywhere / deny, or "approve all"), plus rule suggestions ("approved 3× in 7 days, always allow?").
- Rules are listed in Settings → Permissions (everywhere) and Project settings → Permissions, delete-only, and never created implicitly.

### Tenancy and auth
- Workspace (reserved) → User → Project (a default "Personal" project) → Session.
- Invite-only login with an emailed 6-digit code (the email also has a link) or Google; no passwords ([ADR 0007](./adr/0007-own-passwordless-auth-in-go.md), [auth-keys.md](./design/auth-keys.md)).
- **Provider Keys** are encrypted with the master key and decrypted only in the worker. They are never logged or sent to the frontend.

### Frontend (React PWA)
- Chat streamed over SSE, with `Last-Event-ID` resume, plus one per-user Activity Stream for sidebar badges ([streaming.md](./design/streaming.md)). Commands are sent as POST requests.
- Collapsed step cards by default, with a details toggle for the full trace and thinking. Child sessions appear as nested cards.
- **Slash Commands**: `/remote`, `/stop`, `/model`, `/agent`, `/skill`, `/mode`, `/compact`, `/cost`, `/background`. Each Skill appears as its own slash command; agents are picked with `/agent`. One server-side registry, shared with the CLI.
- **Uploads** and an **Artifacts** panel, backed by S3-compatible storage (MinIO locally).
- Usage dashboard: tokens by Provider, model and token class with estimated $ (to compare with the Provider's own console), auto mode checks, storage; per session, project and user ([usage-metering.md](./design/usage-metering.md)). Sandbox compute comes with the Sandbox.
- Admin-set **Quotas**: monthly auto mode checks and storage (web search and sandbox Quotas later).

### Notifications
- **Channels** built on an interface that supports two-way approvals.
- Base version: PWA push (tap opens the approval card) and email (a signed link opens a page with Approve once / Deny). Telegram right after the base version ([notifications.md](./design/notifications.md)).

### Usage and platform services
- LLM use (including Provider built-in web search in the base version) is paid with the user's own key. Compute, Jev and, later, our own web search are metered by the platform and become the basis for billing later. ([ADR 0006](./adr/0006-byok-llm-platform-metered-services.md))
- Compute is sampled from Docker stats and stored as usage events in the event log.

### Data
- Hard delete for sessions and projects, including sandbox volumes and object storage.
- JSON export.

### Quality
- A fake provider for deterministic loop tests, plus golden event-log tests.
- Chaos tests: kill the worker mid-tool-call and assert the session resumes correctly.
- Record/replay cassettes of real sessions.
- An eval suite (tasks with checkers or an LLM judge; tracks pass rate, cost and turns), run on demand.
- OpenTelemetry tracing: one span per LLM call and per tool call.

## Later roadmap
- Cron and webhook triggers (scheduled/background tasks).
- A Go CLI, including tool execution on the user's own machine.
- A Kubernetes sandbox backend and custom images per project.
- An egress allowlist for sandboxes.
- Semantic memory (pgvector).
- MCP resources and prompts (prompts may later appear as slash commands).
- Local (stdio) Connectors inside the Sandbox.
- On-demand tool search for large Connector catalogs.
- Teams/workspaces and sharing agents.
- More providers: GLM, Ollama and others.
- WhatsApp channel and a mobile app.
- Billing for compute, Jev and web search.
