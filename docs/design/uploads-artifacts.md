# Uploads and Artifacts: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): a file the user attaches to a Session is an **Upload** (never attachment or document); a file the agent produces for download is an **Artifact** (never output or export); a file shared across a Project's sessions is a **Project File** (never knowledge or project upload); the isolated per-session container is the **Sandbox** (never computer, VM, box). Consistent with [provider-gateway.md](provider-gateway.md) §4.3 (`read_upload` tool), §5.4 (Upload projection swap rule), Decisions 9/11/22/23 (inline-only uploads, error class `too_large`, `upload.deleted`, project files index), [context.md](context.md) §5.1–§5.3 (prompt layout, Upload and project-files-index projection), [event-log.md](event-log.md) (`blob_ref` shape, hard-delete rules), ADR [0003](../adr/0003-nothing-executes-on-the-host.md) (nothing executes on the host — Sandbox-only for binary Artifact sources), ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (Quotas exist from day one). Background and prior-art survey: [../research/uploads-artifacts.md](../research/uploads-artifacts.md).

## 1. Summary

- Uploads always travel inline as base64, never through a Provider Files API (provider-gateway.md Decision 11). The prompt projection swaps an Upload for a stub after the user has sent 3 more messages, or at Compaction if that comes first, and never mid tool loop; the agent re-reads it with `read_upload(id, pages?)`.
- A per-request inline budget (24 MB of base64 Upload content, starting value) is checked and enforced the same deterministic way the N=3 swap is: over budget, the oldest inline Uploads are stubbed early.
- Accepted types are PDF, images (PNG/JPEG/WebP/GIF), and text-like files (txt/md/csv/json/code) — native on all three Providers, no conversion step. DOCX/XLSX/PPTX, archives, and SVG are rejected until the Sandbox exists.
- Per-file caps (real bytes, not base64): 10 MB for PDFs and text, 7 MB for images, checked once at attach time; the adapter-level `too_large` error class stays as a safety net for the rare case a Provider's own request cap is hit anyway (e.g. after a `/model` switch).
- Deleting an Upload writes `upload.deleted`, removes the blob at once, and turns any further `read_upload` call into an error result; message chips are preview-only, delete lives in the session Files panel (Uploads / Artifacts tabs).
- `save_artifact(name, content?, path?)` writes a blob + `artifact.saved`, shows a card in chat and the Artifacts tab; saving the same name again creates a new version (v1, v2, …, latest on top). Only the user moves an Artifact into Project Files, with a "Save to project" button; the agent can't.
- Project Files are managed in project settings, indexed in the prompt (name, id, size), and read the same way as Uploads via `read_upload`; their blobs live under a project-level storage location so a session deletion never removes them.
- A per-user storage Quota (default 1 GB, admin-set) covers Uploads, Project Files, and Artifacts together, measured in bytes stored.
- Previews (images, PDFs, text) render client-side from a separate cookie-less origin with `X-Content-Type-Options: nosniff`; there are no server-generated thumbnails.

## 2. Scope

**In the base version**
- Upload attach transport: cap check, magic-byte type check, sha256, object-storage write (D12).
- The `uploads`, `project_files`, and `artifacts` metadata tables (§3).
- The `read_upload(id, pages?)` tool's page-range behavior for large PDFs (D10), and its interaction with the D8 inline budget (D11).
- The D8 inline-budget stub rule, layered on top of provider-gateway.md's N=3 swap rule.
- `save_artifact(name, content?, path?)`, its versioning, and the user-only "Save to project" copy.
- Accepted/rejected file types (D6) and per-file size caps (D7).
- The per-user storage Quota (D13) and its user-facing message.
- The separate preview origin (D14) and its headers.
- New event types `upload.deleted`, `artifact.saved`, `artifact.deleted`.
- The session Files panel (Uploads / Artifacts tabs) and project-settings Project Files list.

**Out (deferred)**
- Provider Files API for Uploads (provider-gateway.md Decision 11; not revisited here).
- Presigned/resumable (tus) upload transport — the base version streams the Upload through our Go server (D12).
- DOCX/XLSX/PPTX, archives (zip/tar), and SVG support — blocked on the Sandbox (D6).
- RAG / embeddings over Project Files (D17).
- Server-side thumbnail generation (D14 — previews are rendered client-side, not thumbnailed).
- Where the Quota counter physically lives: decided in [usage-metering.md](usage-metering.md) — no cached counter; the live-bytes query runs on each check, limit from `user_quotas` or config.
- Tuning the 24 MB budget (D8) and N=3 with evals, once there is real usage.
- Office/SVG/archive support revisits when the Sandbox exists (D6); RAG revisits on the D17 trigger; presigned/tus if Uploads grow past tens of MB (D12).

## 3. Data model

```sql
CREATE TABLE uploads (
  id            text PRIMARY KEY,              -- e.g. "up_7"
  session_id    uuid NOT NULL REFERENCES sessions,
  name          text NOT NULL,                  -- original filename
  mime          text NOT NULL,                  -- sniffed from magic bytes (D12), not the declared content-type
  size          bigint NOT NULL,                 -- real bytes, checked against D7 at attach
  sha256        text NOT NULL,
  blob_key      text NOT NULL,                   -- ws/{workspace_id}/sess/{session_id}/blobs/{sha256} (event-log.md §3)
  page_count    int,                             -- PDFs only; computed at attach (D10)
  created_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz                      -- set by upload.deleted (D2); blob removed at the same time
);

CREATE TABLE project_files (
  id            text PRIMARY KEY,
  project_id    uuid NOT NULL REFERENCES projects,
  name          text NOT NULL,
  mime          text NOT NULL,
  size          bigint NOT NULL,
  sha256        text NOT NULL,
  blob_key      text NOT NULL,                   -- ws/{workspace_id}/proj/{project_id}/files/{sha256} (D23)
  source        text NOT NULL CHECK (source IN ('user_added', 'saved_from_artifact')),
  added_by      uuid NOT NULL REFERENCES users,
  created_at    timestamptz NOT NULL DEFAULT now(),
  deleted_at    timestamptz                      -- D21, D24: blob removed, row kept forever for replay
);

CREATE TABLE artifacts (
  id            text PRIMARY KEY,                -- one row per Artifact; versions below
  session_id    uuid NOT NULL REFERENCES sessions,
  name          text NOT NULL,
  deleted_at    timestamptz,                     -- D15: deletes every version together
  UNIQUE (session_id, name)
);

CREATE TABLE artifact_versions (
  artifact_id   text NOT NULL REFERENCES artifacts,
  version       int NOT NULL,                    -- 1, 2, …; latest shown on top
  mime          text NOT NULL,
  size          bigint NOT NULL,
  sha256        text NOT NULL,
  blob_key      text NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (artifact_id, version)
);
```

- `uploads`/`artifacts` blobs use the existing session-prefixed blob path (event-log.md §3); `project_files` blobs use a separate project-level prefix so a session hard-delete (event-log.md §5.13) never touches them (D16).
- These tables are metadata for the Files panel and project settings list; the neutral `msg.Part.Blob` (`BlobRef`, provider-gateway.md §3.1) is what actually travels to the model, unchanged from event-log.md's existing `blob_ref` shape.
- No `quota_usage` table here: the byte total is a query over the non-deleted rows of `uploads`, `project_files`, and `artifact_versions` for a user; where a live counter is cached is Usage metering's concern (§2).

## 4. Contracts

### 4.1 `read_upload` tool

Already declared in [provider-gateway.md §4.3](provider-gateway.md#43-read_upload-tool); this spec fixes the parts that doc left open:

```go
package upload

// ReadUpload serves the built-in read_upload(id, pages?) tool for both
// Uploads and Project Files (same tool, same BlobRef path).
func ReadUpload(ctx context.Context, id string, pages *PageRange) (ToolResult, error)

type PageRange struct{ From, To int } // 1-based, inclusive; nil Pages arg = first 100 pages + total count (D10)
```

- Text files: `TextPart` with the file's text.
- Image/PDF, no `pages`: the file itself as image/file `Part`s, plus — for a PDF over 100 pages — a note stating the total page count (D10).
- Image/PDF, `pages` given: the server cuts that page range with a pure-Go PDF library at call time, under a timeout and memory cap (D26), and returns only those pages.
- A deleted Upload (`upload.deleted`): error result, `"File was deleted by the user"` (provider-gateway.md §4.3; D2).
- Over budget (D22): if the result would push the inline total past 24 MB, an error result instead: `"Too much file content in view (X MB). Ask for fewer pages."`
- A deleted Project File: error result, `"File was deleted by the user"` (D21).
- The result always carries our own `BlobRef`, never a Provider file id (provider-gateway.md Decision 11).

### 4.2 `save_artifact` tool

```go
package artifact

func SaveArtifact(ctx context.Context, name string, content *string, path *string) (ToolResult, error)
```

- Exactly one of `content` (text, no Sandbox needed) or `path` (a Sandbox container path, copied out; binary OK) is set.
- Writes a blob, then `artifact.saved`; the chat gets a card, the Artifacts tab a row.
- Calling it again with the same `name` in the same session creates the next version of the same Artifact (`version = max(version) + 1`); versions are kept, latest on top (D4).
- Cap: 50 MB (D19), checked before the blob write; Provider per-file limits (D7) don't apply — an Artifact is downloaded, never sent to a model.

### 4.3 Upload attach and type/size validation

```go
package upload

type AcceptedKind string // "pdf" | "image" | "text" (D6)

// Validate checks declared type, sniffed magic bytes, and the D7 per-file
// cap (real bytes) before any blob write. Returns a user-facing message
// naming an alternative format on a type rejection (D6).
func Validate(declaredMime string, sniffedMime string, size int64) (AcceptedKind, error)

var perFileCap = map[AcceptedKind]int64{
    "pdf":   10 << 20, // 10 MB
    "text":  10 << 20, // 10 MB
    "image": 7 << 20,  // 7 MB
} // D7; real size, not base64
```

- Text-like (D25): accepted by content — valid UTF-8, no NUL bytes — not by extension.
- Rejected types (D6): DOCX/XLSX/PPTX, archives (zip/tar), SVG. Example message: `"Excel files aren't supported yet. Export as CSV or PDF."`
- `too_large` (provider-gateway.md §9 Decision 9) is the adapter-side error class shown when a Provider's own request cap is hit despite passing attach-time validation (D9) — e.g. after a `/model` switch to a Provider with a smaller inline ceiling.

### 4.4 Inline budget

```go
package upload

// InlineBudget is a pure function of the projected Uploads still within the
// N=3 swap window (provider-gateway.md §5.4) and their base64 sizes. It
// returns the subset to stub early, oldest-attached first, so the remaining
// total base64 size is ≤ Budget.
func InlineBudget(candidates []InlineCandidate, budget int64) (stub []string /* upload IDs */)

const DefaultInlineBudgetBytes = 24 << 20 // 24 MB, starting value (D8)

type InlineCandidate struct {
    UploadID string
    Base64Size int64 // ceil(realSize/3)*4
    AttachedAtSeq int64 // for oldest-first ordering
}
```

- Pure function of stored events (attach order, current user-message count, latest Compaction) — deterministic and replay-exact, exactly like the N=3 swap rule it composes with (D8).
- Applies to Uploads and to standing `read_upload` results the same way (D11).

## 5. Algorithms and flows

### 5.1 Upload attach (transport, D12)

1. Client sends the file to our Go server (no presigned URL or tus in the base version — deferred, §2).
2. Server checks the declared type and size against `Validate` (§4.3) before reading the whole body past the cap.
3. Server sniffs magic bytes and confirms they match the declared type; a mismatch is treated as a rejection, not a silent re-type.
4. The per-user storage Quota (§5.9) is checked before the blob write; a would-be-over-Quota attach is rejected with the Quota message (D13), not partially written.
5. Server computes sha256 while streaming to object storage, writes the blob, then inserts the `uploads` row.
6. For a PDF, page count is computed at this point and stored on the row (D10) — not at first `read_upload` call.
7. The Upload's `BlobRef` is attached to the `user.message` event the same turn (event-log.md's existing `blob_ref` shape; provider-gateway.md §7 — this is not a separate event; see §7 below).

### 5.2 Inline budget vs. the N=3 swap rule (D1, D8, D9)

1. At every user-message boundary (and at Compaction), first apply provider-gateway.md §5.4's N=3 rule: an Upload attached at message *k* stays inline through message *k+3*; from then on it's a stub, unless it was deleted (swap at once, D2).
2. Among the Uploads the N=3 rule still says should be inline, sum their base64 sizes (`ceil(realSize/3)*4`).
3. If that sum exceeds the inline budget (24 MB, D8), stub the oldest-attached inline Uploads — not the ones the N=3 rule would keep longest — one at a time, until the sum is ≤ budget. This can stub an Upload before its N=3 window would otherwise expire.
4. Both rules are pure projections of the log; neither is its own event. The swap never happens mid tool loop (unchanged from provider-gateway.md §5.4).
5. The cap in §4.3 is checked only at attach time (D9); the budget in this section is recomputed at every boundary, because it depends on how many other Uploads are currently inline, not on any single file's own size.

### 5.3 `read_upload` and the budget (D10, D11)

1. A `read_upload` call returns fresh content as tool-result parts (provider-gateway.md §4.3).
2. That result itself counts toward the same inline budget as an inline Upload, and is stubbed after 3 user messages, oldest first, by the same rule as §5.2 (D11).
3. Before returning, `read_upload` checks the inline total at call time; if the result wouldn't fit the budget it returns the over-budget error result instead (D22), so the prompt never changes mid tool loop.
4. `pages`: the server extracts only the requested range at call time (§4.1); with no `pages`, the model gets the first 100 pages plus the total page count, so it knows to ask for more (D10). Page count itself was already computed at attach (§5.1 step 6), not recomputed per call.

### 5.4 Upload deletion (D2)

1. User deletes from the session Files panel (Uploads tab) — never from a message chip, which is preview-only.
2. Server appends `upload.deleted{upload_id}`, removes the blob, and sets `uploads.deleted_at`.
3. The chat chip immediately shows "(deleted)".
4. The next projection turn swaps the Upload for a deleted stub at once, no N=3 wait (provider-gateway.md §5.4 step 8).
5. Any further `read_upload(id)` call returns an error result, `"File was deleted by the user"`.

### 5.5 Project Files (D3, D16, D18, D21)

1. Added in project settings, not in chat; same accepted types (D6) and per-file caps (D7) as Uploads.
2. The prompt carries only an index — name, id, size (context.md §5.2; provider-gateway.md Decision 23) — never file contents; the model reads one via `read_upload`, same tool as an Upload.
3. The index for a turn is the rows with `created_at` before, and `deleted_at` null or after, that turn's `turn.started` time (D24). Replay rebuilds it exactly.
4. Blobs live under `ws/{workspace_id}/proj/{project_id}/files/{sha256}` (D16, D23), so a session's hard delete (event-log.md §5.13) never removes them.
5. Deleting one happens in project settings; the index changes starting the next turn (one accepted cache break, same as any project-files-index change, D3); `read_upload` then returns `"File was deleted by the user"` (D21). This is recorded as a project change, not a session event: the row keeps its `deleted_at` (D24).

### 5.6 `save_artifact` (D4, D19)

1. Exactly one of `content` (text) or `path` (a Sandbox container path, copied out — binary OK, ADR 0003: nothing executes on the host, so the copy itself is the only host-side step) is given.
2. Checked against the 50 MB Artifact cap (D19) and the per-user storage Quota (§5.9) before the blob write.
3. Writes the blob, then `artifact.saved`; a card appears in chat and a row in the Artifacts tab.
4. Same `name` again in the same session: a new `artifact_versions` row for the same Artifact, `version = max(version) + 1`; all versions are kept, latest shown on top (D4).

### 5.7 "Save to project" (D5, D20)

1. Only a user action, from the Artifacts tab or an Artifact card — the agent has no tool for this (D5).
2. Copies the Artifact's *latest* `artifact_versions` row into a new `project_files` row (`source = 'saved_from_artifact'`); it is a one-time copy — later Artifact versions never update the Project File copy (D20).
3. The copy must pass the same type (D6) and size (D7) checks a directly-added Project File would; a copy that would fail (e.g. an Artifact type not in D6's accepted list) is refused with the same rejection message as §4.3.

### 5.8 Artifact deletion (D15)

1. From the Artifacts tab, hard-deletes the Artifact with all its versions: `artifact.deleted{artifact_id}`, every version's blob removed, `artifacts.deleted_at` set.
2. The card shows "(deleted)".
3. No auto-purge otherwise; a session's own deletion already removes its Artifacts (existing event-log.md hard-delete behavior, unchanged here).

### 5.9 Quota enforcement (D13)

1. Quota is per user, default 1 GB, admin-set (CONTEXT.md: Quota), covering the sum of non-deleted bytes across `uploads`, `project_files`, and `artifact_versions` — not session token/dollar Usage, and not a `usage.recorded` row (D13 is explicit that this is a different axis from Usage).
2. Checked before each blob write (Upload attach §5.1, Project File add §5.5, `save_artifact` §5.6); a would-be-over-Quota write is rejected, not partially written.
3. User-facing message: `"You've used 1 GB of 1 GB. Delete files in the Files panel."`

### 5.10 Preview serving (D14)

1. Previews render client-side: `<img>` for images, the browser's built-in viewer for PDFs, plain text for text-like files — no server-generated thumbnails.
2. Served from a separate, cookie-less origin (e.g. `files.<domain>`) with `X-Content-Type-Options: nosniff`, so a malicious file can't execute in the main app's origin.
3. Applies identically to Artifact previews (D14).

## 6. Rules and invariants

- Uploads are always inline base64; no Provider Files API in the base version (provider-gateway.md Decision 11).
- The N=3 swap and the D8 inline-budget stub are both pure projections of the Event Log; neither is its own event, and neither happens mid tool loop.
- The per-file size cap (D7) is checked once, at attach time only (D9); the inline budget (D8) is recomputed at every user-message boundary and at Compaction.
- `too_large` is a safety net for a Provider-level request cap being hit despite passing attach-time validation — never the primary enforcement (D9).
- A rejected file type always suggests a workable alternative in its message (D6).
- Message chips are preview-only; deleting an Upload or an Artifact only happens from the Files panel / Artifacts tab (D2, D15).
- Only the user can move an Artifact into Project Files; the agent has no tool for it (D5).
- "Save to project" copies the Artifact's latest version once; it never tracks later versions (D20).
- A Project File's blob lives under a project-level storage prefix, never the session prefix, so session deletion never removes it (D16).
- The per-user storage Quota covers Uploads, Project Files, and Artifacts together, measured in bytes stored — distinct from per-session Usage (D13).
- No server-side thumbnails; all previews render client-side from a separate cookie-less origin (D14).
- `read_upload` results are subject to the same inline budget and 3-message stub rule as an inline Upload (D11).
- Deleting an Upload swaps its projection for a deleted stub immediately, without waiting for the N=3 window (provider-gateway.md §5.4 step 8; D2).

## 7. Events

Uploads themselves are not a distinct attach event: an Upload's arrival rides on the `user.message` event's existing `blob_ref` field (event-log.md §3; provider-gateway.md §7 — "Upload refs are the same `BlobRef`s the projection later swaps for a stub"). This spec adds three new event types:

| Event | Payload | Who writes | Fenced | Status effect |
|---|---|---|---|---|
| `upload.deleted` | `upload_id` | API | no | — (projection only; D2) |
| `artifact.saved` | `artifact_id, name, version, blob_ref` | Worker | yes | — (D4) |
| `artifact.deleted` | `artifact_id` | API | no | — (projection only; all versions, D15) |

Project File additions/deletions are project settings changes, not Session events — they are recorded by `project_files.created_at`/`deleted_at` (D24), so no event type is needed.

## 8. UI

- **Session Files panel**: two tabs, Uploads and Artifacts (D2). Uploads tab: name, size, delete. Artifacts tab: name, versions (latest on top), "Save to project" per Artifact, delete (all versions together).
- **Message chip**: preview-only; shows "(deleted)" once the Upload or Artifact behind it is deleted. No delete action on the chip itself.
- **Rejection message** (D6): e.g. `"Excel files aren't supported yet. Export as CSV or PDF."`
- **Over-cap message at attach** (D7): e.g. "Files up to 10 MB. Try compressing the PDF." (images: 7 MB).
- **Provider `too_large` safety net** (D9, provider-gateway.md §9 Decision 9): "File too large" → Remove file.
- **PDF paging note** (D10): "PDFs up to 100 pages are read whole; longer ones are read in parts."
- **Quota banner** (D13): "You've used 1 GB of 1 GB. Delete files in the Files panel."
- **Project settings → Project Files**: add (subject to D6/D18, D7), list (name, size, added by, source), delete.
- **Artifact card in chat**: appears on `artifact.saved`; opens the Artifacts tab entry; "Save to project" button (user-only, D5).
- **Previews**: rendered from the separate cookie-less origin (D14); no distinct UI beyond the standard `<img>`/PDF viewer/plain-text rendering.

## 9. Decisions

All decided 2026-09-27.

1. **Uploads always inline, no Provider Files API** (inherited, [provider-gateway.md](provider-gateway.md) Decision 11). The projection swaps an Upload for a stub after N=3 user messages, or at Compaction if earlier, never mid tool loop; the agent re-reads via `read_upload(id, pages?)`.
2. **Delete Upload** (inherited, [provider-gateway.md](provider-gateway.md) Decision 22). `upload.deleted{upload_id}`, blob removed, chip shows "(deleted)", deleted stub at once, `read_upload` returns an error result. Message chips are preview-only; delete lives in the session Files panel (Uploads, Artifacts tabs).
3. **Project Files index only in the prompt** (inherited, [provider-gateway.md](provider-gateway.md) Decision 23). Read via `read_upload`; managed in project settings; index changes break the cache once (accepted).
4. **`save_artifact(name, content?, path?)`** (inherited, decided 2026-09-27). Text content or a Sandbox container path (copied out, binary OK); writes a blob + `artifact.saved`, card in chat, Artifacts tab row. Same name again = a new version of the same Artifact (v1, v2 kept, latest on top).
5. **Only the user moves an Artifact into Project Files** (inherited, decided 2026-09-27), via "Save to project"; the agent has no tool for it.
6. **Accepted types**: PDF, images (PNG/JPEG/WebP/GIF), text-like (txt/md/csv/json/code). Rejected until the Sandbox exists: DOCX/XLSX/PPTX, archives (zip/tar), SVG. Rejection message suggests an alternative format. Reason: native inline support on all three Providers with no conversion step; SVG/archives excluded for security (script-in-SVG, zip bombs).
7. **Per-file cap** (real size, not base64): 10 MB for PDFs and text, 7 MB for images, same on every Provider. Reason: Provider limits count base64 (~4/3 inflation); Anthropic's 32 MB whole-request cap and 10 MB base64-per-image cap are the tightest.
8. **Inline budget**: total base64 size of Uploads currently inline in a projected prompt ≤ 24 MB (starting value, tunable). Over budget, the oldest inline Uploads are stubbed early, oldest first — a deterministic, pure function of events, like the N=3 swap rule. Reason: two large Uploads inside the 3-message window could otherwise exceed Anthropic's 32 MB request cap.
9. **Cap checked only at attach time.** `too_large` stays as a safety net ("File too large" → Remove file) for the rare case a Provider's request cap is still hit.
10. **`read_upload` `pages`**: the server cuts the requested page range at call time with a pure-Go PDF library, under a timeout and memory cap. No `pages` → first 100 pages plus the total page count. Page count is computed at attach. UI: "PDFs up to 100 pages are read whole; longer ones are read in parts." Reason: Anthropic's 100-page-per-request limit when the context window is under 1M tokens; ADR 0003 covers tools, not the server reading a file, so it doesn't forbid this.
11. **`read_upload` results count toward the D8 budget** and are stubbed after 3 user messages, oldest first, same as an inline Upload.
12. **Transport**: the Upload goes through our Go server — cap check, magic-byte check against the declared type, sha256, write to object storage. Presigned URLs and tus/resumable upload are deferred.
13. **Per-user storage Quota** covering Uploads, Project Files, and Artifacts, measured in bytes stored — not per-session Usage, not a `usage.recorded` row. Default 1 GB, admin-set. Example message: "You've used 1 GB of 1 GB. Delete files in the Files panel."
14. **Previews render client-side** (`<img>`, the built-in PDF viewer; text as plain text) from a separate cookie-less origin (e.g. `files.<domain>`) with `X-Content-Type-Options: nosniff`; no server-generated thumbnails. Precedent: claude.ai serves Artifacts from a sandboxed `*.claudeusercontent.com` origin. Applies to Artifact previews too.
15. **Artifacts hard-deleted from the Artifacts tab**, all versions of a name together → `artifact.deleted{artifact_id}`, card shows "(deleted)". No auto-purge. A session's own deletion still removes its Artifacts (existing event-log.md behavior).
16. **Project File is its own term**, separate from Upload (session-only). Project File blobs live under a project-level storage location, not the session prefix, so session deletion never removes them. Key path: D23.
17. **No RAG in the base version.** Revisit when: Projects regularly holding more files than the model can browse (e.g. 50+ Project Files), or users reporting misses.
18. **Project Files added in project settings follow the same accepted types (D6) and per-file caps (D7)** as Uploads.
19. **Artifact cap: 50 MB**; counts toward the D13 Quota. Provider per-file limits (D7) don't apply — Artifacts are downloaded, never sent to a model.
20. **"Save to project" copies the Artifact's latest version** into a new Project File — a one-time copy; later Artifact versions never update it. The copy must still pass D6/D7.
21. **Deleting a Project File**: in project settings; the index changes starting the next turn (one accepted cache break, D3); `read_upload` returns `"File was deleted by the user"`. Recorded as a project change, not a session event.
22. **`read_upload` over budget**: checked at call time; if the result would push the inline total past 24 MB, it returns an error result ("Too much file content in view (X MB). Ask for fewer pages.") instead. The prompt never changes mid tool loop.
23. **Project File storage path**: `ws/{workspace_id}/proj/{project_id}/files/{sha256}`, mirroring the session path. Deleting the project deletes that prefix.
24. **Project File changes and replay**: deleting a Project File removes its blob but keeps its row with `deleted_at`. The index for a turn is the rows that existed at that turn's `turn.started` time, so replay rebuilds the same prompt. No new event and no audit log in the base version.
25. **Text-like means content, not extension**: any valid UTF-8 file with no NUL bytes, up to 10 MB, is accepted as text (so `Makefile`, `main.rs` work; a renamed binary fails).
26. **PDF page cutting**: `pdfcpu` (pure Go) with a 10 s timeout; the memory cap is set when built.
27. **Deferred items are Scope, not gaps**: budget/N tuning, RAG, presigned/tus, Office/SVG/archives, and the Quota counter (Usage metering) are listed in §2 Out with their triggers.

## 10. Edge cases

- **a.pdf (9 MB) attached at message 1, b.pdf (9 MB) at message 2, c.pdf (5 MB) at message 3**: all three are still within their individual N=3 windows at message 3, but their combined base64 size (≈30.7 MB) exceeds the 24 MB budget; a.pdf, the oldest, is stubbed early even though its own N=3 window wouldn't expire until message 4 (§11 has the arithmetic).
- **An Upload is deleted while still inside its N=3 window**: the deleted-stub swap (D2) preempts the window; it never waits for message *k+3*.
- **`read_upload(pages="1-5")` on a 40-page PDF**: returns only pages 1–5; no full-document fallback.
- **A PDF with 150 pages, no `pages` arg**: the model gets pages 1–100 plus a note that the document has 150 pages total, so it knows to ask for the rest.
- **`/model` switch to a Provider with a smaller inline request cap after Uploads were already attached**: attach-time validation isn't re-run; the next turn can still hit `too_large` (the safety net, D9).
- **An SVG upload**: rejected outright — SVG is excluded even though it would otherwise fit the "image" category, because it can carry a script (D6).
- **A user at their 1 GB Quota tries to attach a new Upload**: the attach is rejected before the blob write, with the Quota message (D13); nothing partial is written.
- **`save_artifact("report.md", content=...)` called three times in one session**: three versions (v1, v2, v3) exist; the Artifacts tab shows v3 on top; all three remain until the Artifact is deleted.
- **"Save to project" on an Artifact, then the agent saves a v2 of that Artifact**: the Project File copy still reflects v1; nothing updates it automatically (D20).
- **A Project File is deleted mid-session**: the running session's prompt still shows the old index until its next turn (one cache break); a `read_upload` call after the delete returns "File was deleted by the user" regardless of when the index itself refreshes.
- **`read_upload` mid tool loop with 20 MB already inline, on a 9 MB PDF**: returns the over-budget error result (D22); the model asks for fewer pages.
- **Replay after a Project File was deleted**: a turn that started before the delete still lists the file in the rebuilt prompt; later turns don't (D24).
- **Two Uploads with identical base64 size hit the budget boundary simultaneously**: tie-broken by attach order (the older, i.e. lower `AttachedAtSeq`, is stubbed first) — deterministic and replay-exact.

## 11. Acceptance criteria

- a.pdf (9 MB) at message 1, b.pdf (9 MB) at message 2, c.pdf (5 MB) at message 3: base64 sizes are `ceil(9,000,000/3)*4 = 12,000,000` bytes (a.pdf, b.pdf) and `ceil(5,000,000/3)*4 = 6,666,668` bytes (c.pdf); all three inline sums to ≈30.67 MB > the 24 MB budget; removing a.pdf (the oldest) brings the total to ≈18.67 MB, under budget — so a.pdf, and only a.pdf, is stubbed in the projected prompt for message 3, while b.pdf and c.pdf stay inline. The Event Log's stored `user.message` events are unchanged before and after.
- An 11 MB PDF is rejected at attach time with the over-cap message ("Files up to 10 MB…"), and no blob is written; a 9 MB PDF is accepted.
- An 8 MB image is rejected (over the 7 MB image cap) even though an 8 MB PDF would be accepted (under the 10 MB PDF/text cap).
- A `.docx` upload attempt is rejected with a message naming an alternative format (e.g. "Export as CSV or PDF"), and no blob is written.
- A 150-page PDF's `read_upload()` call with no `pages` argument returns exactly the first 100 pages plus a note stating the document has 150 pages total.
- Deleting an Upload writes `upload.deleted{upload_id}`, removes its blob immediately, shows "(deleted)" on its chat chip, and makes the next `read_upload(id)` call return an error result — even if that Upload was still inside its N=3 inline window.
- `save_artifact("report.md", content="v1 text")` then `save_artifact("report.md", content="v2 text")` in the same session produces one `artifacts` row and two `artifact_versions` rows (1 and 2); the Artifacts tab lists v2 above v1.
- "Save to project" on an Artifact copies only its current latest version into a new `project_files` row; a later `save_artifact` call under the same name does not change that Project File's row.
- Deleting an Artifact from the Artifacts tab removes every version's blob, sets `artifacts.deleted_at`, and writes one `artifact.deleted{artifact_id}` event; the card shows "(deleted)".
- A user whose Uploads + Project Files + Artifacts already total 1 GB gets the Quota message and a rejected attach on the next Upload, with no partial blob written.
- A preview request for an Upload or Artifact is served from the separate preview origin with `X-Content-Type-Options: nosniff` and no cookies attached.
- Deleting a Project File in project settings makes the next turn's project-files index omit it, and any `read_upload` call against its id (from before or after that turn) returns "File was deleted by the user".
- With a.pdf and b.pdf (9 MB each, ≈24 MB base64) inline, `read_upload` on another 9 MB PDF inside the same tool loop returns "Too much file content in view…"; the prompt's stubs are unchanged until the next user message.
- A Project File deleted at 10:05: replaying a turn whose `turn.started` is 10:04 produces a prompt whose index still lists it; a turn at 10:06 doesn't.
- A file named `notes.txt` containing NUL bytes is rejected; a `Makefile` with valid UTF-8 is accepted as text.

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

Provider inline-size limits and file-type support per Provider, storage architecture (blob store, presigned URLs, tus), prompt-injection and zip-bomb/SVG-XSS security background, Artifact-versioning precedent (Claude.ai, ChatGPT Canvas), and full source list: [../research/uploads-artifacts.md](../research/uploads-artifacts.md).
