# TODO

## Design docs (`docs/design/<feature>.md`)

Each feature gets two docs:
- `docs/research/<feature>.md`: background, prior art, options, sources. Not read during implementation.
- `docs/design/<feature>.md`: the spec only (scope, data model, contracts, flows, rules, decisions, edge cases, acceptance criteria). This is what implementers read.

Flow: research → grill the open questions → write the spec.

- [x] Event log and durability: `docs/design/event-log.md`
- [x] Agent loop, steering and interrupt: `docs/design/agent-loop.md`
- [x] Provider gateway (neutral format): `docs/design/provider-gateway.md`
- [x] Memory (user and project memory, tidy, forgetting): `docs/design/memory.md`
- [x] Context and compaction: `docs/design/context.md`
- [ ] Sandbox (**not in the base version**, decided 2026-09-27; design after the base version ships. Research done: [research/sandbox.md](docs/research/sandbox.md), not grilled yet)
  - Next: brief, then Q0 isolation (plain Docker / Docker+gVisor / microVM; gVisor is Linux-only, so dev on macOS would use runc), then research §15 Q1–Q10. Cross-spec conflicts in research §5.1, §8, §10.
  - Must cover: timeout kill of the process group (TERM → grace → KILL, timings), since Docker can't kill an exec (moby#35703). From [agent-loop.md](docs/design/agent-loop.md) Decision 11.
  - Must cover: whether project files appear inside the Sandbox container (e.g. `/project/…`). From provider-gateway grilling.
  - Must cover: local (stdio) Connectors moved here from MCP client (2026-09-27): supervision across a Worker crash and Connector secrets in the Sandbox env (research/sandbox.md §9, §13).
  - Must cover: from Agents grilling (2026-09-27): copying a Skill's files into the Sandbox (path, when), and the Sandbox shared by a session and its Child Sessions (ownership, a child attaching one for the parent) ([agents-skills.md](docs/design/agents-skills.md) D4, D20).
  - Must cover: moved from Approver grilling (2026-09-27): compound shell command splitting (every subcommand must match a rule, like Claude Code), how shell/file tools are approved, and destructive-shell exceptions in full-auto.
- [x] MCP client and OAuth: `docs/design/mcp-client.md`
- [x] Approver and permissions (incl. Jev): `docs/design/approver.md`
- [x] Uploads and artifacts: `docs/design/uploads-artifacts.md`
  - Decided 2026-09-27 (provider-gateway grilling): `upload.deleted{upload_id}` event (chip shows "(deleted)", blob removed); message chips preview-only; delete in session Files panel (tabs: Uploads, Artifacts); project files managed in project settings; project files index in the prompt, read via `read_upload` ([provider-gateway.md](docs/design/provider-gateway.md) Decisions 22–23).
  - `save_artifact(name, content?, path?)`: text content without a Sandbox, or a Sandbox container path (copied out, binary OK); writes blob + `artifact.saved`, card in chat, Artifacts tab. Same name again = new version of the same Artifact (v1, v2 kept, latest on top). Only the user can move an Artifact into project files ("Save to project" button); the agent can't.
  - Designed 2026-09-27: upload flow, size limits, file types, inline budget, storage Quota, Artifact hard delete.
- [x] Agents, child sessions and skills: `docs/design/agents-skills.md`
- [x] Streaming (SSE): `docs/design/streaming.md`
- [x] Usage metering: `docs/design/usage-metering.md`
  - Must cover: Jev Quota reset period, and the reset date on the "auto mode" Quota banner (from Approver grilling, [approver.md](docs/design/approver.md) Decision 22).
  - Must cover: per-user storage Quota counter (bytes stored, Uploads + Project Files + Artifacts, default 1 GB, admin-set) (from Uploads grilling, [uploads-artifacts.md](docs/design/uploads-artifacts.md)).
- [x] Notifications: `docs/design/notifications.md` (PWA push + email; Telegram right after the base version)
- [x] Auth and keys: `docs/design/auth-keys.md`
  - Must cover (from Streaming, 2026-09-27): SSE auth. `EventSource` sends no headers, so cookie auth; Origin check on SSE and POSTs; what happens when the login expires during a long-lived stream ([streaming.md](docs/design/streaming.md), research/streaming.md §8).
  - Decided 2026-09-27: master key from env var / secret file (`JF_MASTER_KEY`), `key_id` on every encrypted row for rotation, cloud KMS later behind the same interface ([mcp-client.md](docs/design/mcp-client.md) Decision 24).
- [ ] Web search (Brave and Tavily): **deferred past the base version** (2026-09-27). Base version uses each Provider's built-in search on the user's key ([provider-gateway.md](docs/design/provider-gateway.md) D6a, ADR 0006 amendment). Brave/Tavily facts checked 2026-09-27: Brave $5/1k, $5 free credit/month, 50 QPS; Tavily $0.008/credit, 1,000 free credits/month; SearXNG is the only open-source option (metasearch, unreliable). Proposed when resumed: Brave default + SearXNG for local dev. Our own `web_fetch` is deferred with it (Provider fetch in the base version); when resumed it must cover SSRF: http/https on 80/443 only, block private/loopback/link-local/metadata IPs, connect to the checked IP, re-check each redirect (≤ 5), 15 s / 5 MB, HTML → text, ~100 KB cap.
  - Must cover (from Usage metering, 2026-09-27): write `usage.recorded{kind: search, provider: brave|tavily, unit: searches}` per request sent; call `Quotas.Check` first (default 300/month); at the limit return the tool error + inline line ([usage-metering.md](docs/design/usage-metering.md) §5.1, §5.3); whether a user's own search key is recorded/limited.
- [ ] Testing and evals

## After design
- [ ] Break the base version into build slices (issues)
  - Each slice: build on desktop browser first, verify on a phone (iOS Safari PWA) before done ([streaming.md](docs/design/streaming.md) D12).
