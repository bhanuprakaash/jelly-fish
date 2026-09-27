# Jelly-fish Agent Runtime

A multi-tenant platform where users chat with any LLM using their own keys. Every chat is a durable agent run that keeps going on the server after the user leaves.

## Language

### Tenancy

**User**:
A person with an account. Owns provider keys, projects, and approval rules.
_Avoid_: Account, customer, member

**Workspace**:
A reserved owner above users, for future teams. It holds exactly one user for now.
_Avoid_: Org, team, tenant

**Project**:
A folder of related sessions that share instructions, project files, connectors, agents, skills, project memory, and approval rules. Every user gets a default "Personal" project.
_Avoid_: Space, folder, repo

**Login Session**:
One signed-in browser or device of a user, stored server-side and revocable ("log out everywhere" deletes them all). Never a chat.
_Avoid_: Session, web session, auth session

### Execution

**Session**:
One conversation and its agent run, owned by a project. Plain chats and long autonomous tasks are both sessions.
_Avoid_: Chat, task, job, thread, conversation

**Trigger**:
What started a session. For now: a user message, or a system job such as Tidy memory. Schedules and webhooks come later.
_Avoid_: Source, origin

**System Session**:
A hidden session the platform starts for a background job (e.g. Tidy memory), so its LLM Usage and events are recorded like any other session. It is not shown in the chat list.
_Avoid_: Background job, internal task

**Background Session**:
A session the user sent to work on its own with `/background`. The Budget's wall-clock limit applies only while it stays in background; it returns to foreground when the agent finishes (the user is notified) or on the user's next message. Distinct from `/remote`, which only attaches a Sandbox.
_Avoid_: Remote session, autonomous mode, detached

**Turn**:
One LLM call inside a session, together with the tool calls it requests.
_Avoid_: Step, iteration, round

**Event Log**:
The append-only, ordered record of everything that happened in a session. It is the only source of truth for the session's state.
_Avoid_: History, transcript, journal

**Worker**:
A runtime process that holds a lease on a session and advances it turn by turn.
_Avoid_: Runner, executor, daemon

**Lease**:
A worker's time-limited, renewable claim to be the only one advancing a session.
_Avoid_: Lock, claim, ownership

**Steering**:
A user message sent while a session is running. It is injected at the next tool-result boundary.
_Avoid_: Interjection, follow-up

**Interrupt**:
A user action that cancels the in-flight LLM or tool call right away.
_Avoid_: Cancel, abort, kill

**Interrupted Tool Call**:
A tool call whose outcome is unknown because its worker died mid-call. The model decides what to do next, and the call is never retried automatically.
_Avoid_: Failed call, orphaned call

**Child Session**:
A session started by another session to do delegated work. Nesting is capped at a maximum depth.
_Avoid_: Subtask, worker agent (the term "sub-agent" is fine in conversation)

**Activity Stream**:
One live feed per open app tab that reports status changes (running, needs approval, failed) for all of a user's sessions, so the chat list can show badges. It carries no chat content.
_Avoid_: Notification feed, inbox, event bus

**Delegation**:
A session starting a Child Session through the `delegate` tool to do part of its work. The child's result comes back to the parent.
_Avoid_: Spawning, handoff

### Agents and capabilities

**Agent**:
A saved configuration: a description of when to use it, instructions, model, allowed tools and connectors, skills, permission mode, and budget. It is saved in one project or everywhere (all of the user's projects); a project one wins over an everywhere one with the same name. "General" is the default agent.
_Avoid_: Bot, assistant, persona

**Default Agent**:
The one Agent a project uses for new sessions and for Tidy memory. It is "General" unless the user changes it.
_Avoid_: Main agent, primary agent

**Skill**:
A reusable pack of instructions (and optional scripts) that the agent loads only when relevant. Like an Agent, it is saved in one project or everywhere. Its scripts can run only in a Sandbox.
_Avoid_: Plugin, template, prompt

**Slash Command**:
A shortcut typed in the chat input, such as `/remote`, `/stop`, `/model`, or `/background`. Each Skill appears as its own slash command automatically; agents are picked with `/agent`.
_Avoid_: Shortcut, macro

**Provider**:
An LLM vendor that the user's own key unlocks, such as Anthropic, OpenAI, or Gemini.
_Avoid_: Vendor, backend, gateway

**Provider Key**:
A user's own API key for a provider (BYOK).
_Avoid_: Token, credential, secret

**Model Catalog**:
The platform's list of known models with their limits, capabilities, and approximate prices. Dollar figures from it are estimates; the provider's invoice is the truth.
_Avoid_: Model registry, price list

**Connector**:
An MCP server attached to a project. It is either remote (a URL, optionally connected through OAuth) or local (a program that runs inside the sandbox).
_Avoid_: Integration, MCP, plugin

**Connector Slug**:
A short name for a Connector, unique within its project and fixed once chosen. It is put in front of the Connector's tool names (`notion__search_pages`).
_Avoid_: Prefix, alias, namespace

**Tool**:
A single action the agent can call. It is either built-in (web search, web fetch, shell, files) or provided by a connector.
_Avoid_: Function, capability, action

**Elicitation**:
A question that a connector asks the user in the middle of a tool call.
_Avoid_: Prompt, form

### Sandbox

**Sandbox**:
The isolated container that belongs to one session and is shared with its Child Sessions. Only here can shell commands, file edits, and local connectors run.
_Avoid_: Computer, VM, box, remote, environment

**Sandboxed Session**:
A session that has a sandbox attached. The sandbox comes from the project default, the agent requesting one, or the `/remote` command.
_Avoid_: Remote session, local session

**Workspace Volume**:
The sandbox's persistent `/workspace` storage. It survives while the sandbox is stopped and is deleted after 7 days idle.
_Avoid_: Disk, mount

### Permissions

**Permission Mode**:
How freely a session may act without asking. The modes are ask, auto (the default), and full-auto.
_Avoid_: Autonomy level, trust level

**Approval**:
A paused decision where a human allows or denies one specific tool call.
_Avoid_: Confirmation, consent, prompt

**Approval Rule**:
A saved "always allow" for one tool, optionally narrowed to specific argument values, that a user created explicitly. It is scoped to one project or to everywhere. Rules are never created implicitly, and there are no deny rules.
_Avoid_: Allowlist entry, permission, grant

**Approver**:
The step that decides allow, ask the user, or deny for a tool call. It runs in order: rules, then Jev; if Jev is unsure or unavailable, it asks the user. There is no other fallback model.
_Avoid_: Guard, gate, classifier

**Jev**:
A decision model provided by the platform (from TypeSafe) that the approver uses. It is metered as platform usage. It is an internal name: users only ever see it as "auto mode".
_Avoid_: Safety model, classifier

### Usage

**Budget**:
Per-session limits: tokens and dollars accumulate over the whole session; turns reset with each user message; wall-clock time (running time only) applies only to Background Sessions, Child Sessions, and System Sessions, never foreground chat. When one is reached, the session pauses and asks for an approval.
_Avoid_: Limit, cap

**Quota**:
A per-user limit on platform resources, set by an admin: monthly counts for Platform Services (auto mode checks; web searches once our own search exists), and bytes stored. Sandbox compute Quotas come with the Sandbox.
_Avoid_: Plan, allowance

**Usage**:
Measured consumption, recorded per session. It covers provider tokens and dollars, sandbox compute, and platform services (Jev, web search).
_Avoid_: Cost, consumption, billing

**Platform Service**:
A capability that the platform pays for and meters, as opposed to one paid with the user's provider key. Jev is a platform service; our own web search will be one after the base version (until then, Provider built-in search on the user's key).
_Avoid_: Managed service, add-on

### Files and output

**Upload**:
A file a user attaches to a session. Only PDFs, images, and text-like files are accepted.
_Avoid_: Attachment, document

**Project File**:
A file in a project's shared set that every session in the project can read. It comes from a user adding it in project settings, or from the user saving an Artifact to the project.
_Avoid_: Knowledge, project upload, document

**Artifact**:
A file the agent produced that the user can download.
_Avoid_: Output, result, export

**User Memory**:
Persistent notes about the user that the agent reads and writes, applying across all their projects. Each project can opt out.
_Avoid_: Profile, personal memory, global memory

**Project Memory**:
Persistent notes the agent reads and writes across sessions within one project.
_Avoid_: Knowledge base, context, notes

**Compaction**:
Replacing older parts of a session's context with a summary when the context window fills up.
_Avoid_: Summarization, truncation, pruning

### Notifications

**Channel**:
A way to reach a user who is away: PWA push or email in the base version, Telegram next. A channel can carry approvals in both directions.
_Avoid_: Notifier, integration
