# Nothing executes on the host; sandboxes are lazy and per session

No tool ever touches the worker's or server's filesystem or processes. Shell, file tools, and local (stdio) **Connectors** run only inside the session's **Sandbox**, reached through a `Sandbox` interface (Docker now, Kubernetes pods later). Remote connectors are called from the worker over HTTP. Most sessions need no sandbox. One is attached lazily: from the project default, when the agent asks for one (behind an approval), or via `/remote`. Idle sandboxes are stopped; the Workspace Volume persists for 7 days.

## Consequences

- A session waiting days on an Approval holds no container and no worker.
- Running a user's own stdio MCP server is only possible in a sandboxed session.

**Amendment (2026-09-27):** a session's Sandbox is shared with its Child Sessions; a child that attaches one attaches it to the parent. Skill scripts run only here. Spec: [agents-skills.md](../design/agents-skills.md) D4, D20.
