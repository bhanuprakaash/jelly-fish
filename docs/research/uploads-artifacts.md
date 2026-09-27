# Uploads and Artifacts: Research

Status: research, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): **Upload** is a file a user attaches to a Project or Session (never "attachment"); **Artifact** is a file the agent produced for download (never "output"); **Project files** are managed in project settings, indexed in the prompt, and read via `read_upload`; **Compaction** replaces older turns with a summary. Ground truth: [provider-gateway.md](../design/provider-gateway.md) Decisions 11, 22–23 (uploads always inline base64, no Provider Files API in base version, Upload projection swap rule, `save_artifact` function, project files index), [context.md](../design/context.md) §5.1–5.2 (prompt layout, Upload projection), [event-log.md](../design/event-log.md) (payload storage, blob_ref rules), [ADR 0006](../adr/0006-byok-llm-platform-metered-services.md) (platform services metering, Quotas from day one).

---

## 1. Summary

- **Provider file APIs are production-ready but jelly-fish defers them**: Anthropic Files API ([platform.claude.com/docs/en/api/files](https://platform.claude.com/docs/en/api/files)), OpenAI Files API (512MB per file, 20MB for Assistants file_search; [developers.openai.com/api/reference/resources/files](https://developers.openai.com/api/reference/resources/files)), and Gemini Files API (2GB uploads, 100MB inline, 50MB for PDFs; [ai.google.dev/gemini-api/docs/files](https://ai.google.dev/gemini-api/docs/files)) all support persistent storage and per-request citations — but provider-gateway.md Decision 11 commits to inline base64 only in the base version ("no Provider Files API") to avoid lock-in and keep the Upload projection swap rule deterministic.
- **The binding limit is each Provider's inline per-request cap, not a Files API or consumer-app limit** (Decision 11: uploads are always inline, no Files API). Anthropic's Messages API caps the *whole request* at 32 MB, images additionally at 10 MB/8000×8000px each ([platform.claude.com/docs/.../vision](https://platform.claude.com/docs/en/build-with-claude/vision), [.../pdf-support](https://platform.claude.com/docs/en/build-with-claude/pdf-support)); OpenAI's Responses API caps file inputs (PDF etc.) at 50 MB combined but images at 512 MB/1500 images ([developers.openai.com/.../file-inputs](https://developers.openai.com/api/docs/guides/file-inputs), [.../images-vision](https://developers.openai.com/api/docs/guides/images-vision)); Gemini's inline-data cap is 100 MB total request (raised from 20 MB, Jan 2026), PDFs additionally capped at 50 MB/1000 pages ([ai.google.dev/.../document-processing](https://ai.google.dev/gemini-api/docs/document-processing), [.../image-understanding](https://ai.google.dev/gemini-api/docs/image-understanding)). Anthropic's 32 MB total-request cap is the strictest of the three — cite this as jelly-fish's design anchor (§9 Q1).
- **Uploads count toward context window at reference time, not upload time**: when an Upload is passed inline (base64) to the model, its tokens are spent; when swapped to a stub (provider-gateway §5.4), no cost is incurred until `read_upload` is called. This interaction with Compaction is critical: a Tier 2 summary that references older uploads may cause the summary text itself to grow if those uploads are still inlined in the compacted history.
- **Artifact versioning is a ship-vs-build decision**: Claude.ai's artifact version history went missing in September 2026, per a user bug report labelled a regression; Anthropic's docs still describe versions, so this is **Unverified** as a deliberate removal ([github.com/anthropics/claude-code/issues/96718](https://github.com/anthropics/claude-code/issues/96718)), and ChatGPT Canvas was removed from its GPT-5.5 chat models in May 2026 in favor of inline writing/code blocks (secondary source quoting OpenAI release notes; [ai-toolbox.co/chatgpt-management-and-productivity/how-to-use-chatgpt-canvas-guide-2026](https://www.ai-toolbox.co/chatgpt-management-and-productivity/how-to-use-chatgpt-canvas-guide-2026)). Neither vendor's current direction favors a rich version-history UI. jelly-fish's `save_artifact(name, content?, path?)` with same-name versioning (v1, v2, latest on top) is simpler than a full revision history and survives the trend of vendors moving *away* from artifact versioning.
- **Security risks are real and specific**: prompt injection via malicious PDF/SVG content — hidden text in a PDF causing an agent to exfiltrate data via a tool call is independently documented ([x.com/simonw/status/1969111931152634010](https://x.com/simonw/status/1969111931152634010)), and prompt injection is OWASP's top-ranked LLM risk, LLM01:2025 ([genai.owasp.org/llmrisk/llm01-prompt-injection](https://genai.owasp.org/llmrisk/llm01-prompt-injection/)) — zip bombs ([mimecast.com/content/what-is-a-zip-bomb](https://www.mimecast.com/content/what-is-a-zip-bomb)), and SVG XSS on inline preview ([github.com/advisories/GHSA-7jp5-298q-jg98](https://github.com/advisories/GHSA-7jp5-298q-jg98)) all have 2026 CVEs and require specific mitigations at upload-validation, preview, and preview-serving layers.

---

## 2. Upload flow: size limits and file types per Provider

Because Decision 11 sends Uploads inline (base64) with no Provider Files API, what binds jelly-fish is each Provider's *inline per-request* limit — not the Files API numbers above, and not the Claude.ai/Projects consumer-app limits (those apply to a different upload path entirely).

### Inline per-request limits (primary sources)

| Provider | Total request (inline) | PDF | Image |
|---|---|---|---|
| **Anthropic** (Messages API) | 32 MB, whole request incl. text; lower on Amazon Bedrock/Google Cloud ([platform.claude.com/.../pdf-support](https://platform.claude.com/docs/en/build-with-claude/pdf-support)) | Same 32 MB cap; max 600 pages/request (100 if context window <1M tokens); no passwords/encryption | Max 10 MB/image base64 (5 MB on Bedrock/GCP); max 8000×8000 px; above 20 images/request a stricter per-image pixel limit applies (resize to ≤2000 px/side to stay safe on all platforms); max 100 images/request (200k-context models) or 600 (others) — the 32 MB total usually binds first ([platform.claude.com/.../vision](https://platform.claude.com/docs/en/build-with-claude/vision)) |
| **OpenAI** (Responses API) | 512 MB total image payload; 50 MB combined for non-image file inputs ([developers.openai.com/.../file-inputs](https://developers.openai.com/api/docs/guides/file-inputs), [.../images-vision](https://developers.openai.com/api/docs/guides/images-vision)) | Each file <50 MB; combined file total ≤50 MB/request | Total image payload ≤512 MB; ≤1500 images/request; ≤30,000 patches/image after resizing; pixel-dimension cap is model-specific |
| **Gemini** (Generative Language API) | 100 MB total request, inline data + text (raised from 20 MB, Jan 2026; [blog.google/.../gemini-api-new-file-limits](https://blog.google/innovation-and-ai/technology/developers-tools/gemini-api-new-file-limits/)) | ≤50 MB or ≤1000 pages, within the 100 MB inline cap ([ai.google.dev/.../document-processing](https://ai.google.dev/gemini-api/docs/document-processing)) | ≤3600 images/request; token cost per 768×768 px tile ([ai.google.dev/.../image-understanding](https://ai.google.dev/gemini-api/docs/image-understanding)) |

**Side note, consumer apps** (not what binds the API): Claude.ai web chat allows 500 MB per upload; Claude Projects caps at 30 MB per project file ([support.claude.com/en/articles/8241126-upload-files-to-claude](https://support.claude.com/en/articles/8241126-upload-files-to-claude)). Neither figure is a Messages API limit — dropped the earlier "Claude Code CLI 30 MB (API Files)" line; no primary source states that number for the CLI.

### File types

Native inline support differs by Provider, not just by size. Anthropic's Messages API `document` block only accepts `application/pdf` — DOCX/XLSX/PPTX must be converted to PDF or text first ([platform.claude.com/.../pdf-support](https://platform.claude.com/docs/en/build-with-claude/pdf-support)). OpenAI's Responses API accepts DOCX/PPTX (text-extracted) and XLSX (row-parsed, first 1000 rows/sheet) directly as file inputs ([developers.openai.com/.../file-inputs](https://developers.openai.com/api/docs/guides/file-inputs)). Gemini's docs list Office MIME types for inline input, but developer reports describe inconsistent behavior — **Unverified** beyond the documented MIME-type list ([ai.google.dev/.../document-processing](https://ai.google.dev/gemini-api/docs/document-processing)).
**Gemini-only**: audio and video input ([ai.google.dev/gemini-api/docs/files](https://ai.google.dev/gemini-api/docs/files)).

See §9 for the open question on which file types jelly-fish accepts and whether DOCX/XLSX/PPTX get server-side conversion.

---

## 3. How files feed to models: inline vs. tool-based reads

**Three patterns in production**:

1. **Native multimodal parts** (Anthropic, OpenAI Responses, Gemini): file content (base64 image/PDF) embedded directly in the message, counts toward context window at send time. Provider-gateway Decision 11 mandates this for jelly-fish ("uploads always inline").
2. **Tool-based reads** (OpenAI Assistants, Gemini via Files API, future jelly-fish `read_upload`): agent calls a tool to fetch file content on demand; context-window cost is deferred until the tool result is passed back. Anthropic Files API supports both (inline document reference *or* inline content), but Decision 11 defers Files API entirely.
3. **Extracted-to-text** (some integrations): files are OCR'd or parsed to plain text and injected into the system prompt or memory; never exposed as a native file part to the model. jelly-fish's `read_upload` tool handles this for project files (context.md §5.2 index).

**⚠ cross-spec**: provider-gateway Decision 23 says "project files: the prompt carries only an index of project files (name, id, size); the model reads them with `read_upload`" — but for Uploads attached to a Session, Decision 11 says "always inline (base64)" with the swap-rule deferral. The two coexist: Uploads (user-attached, per-session) are inline; project files (admin-managed, per-project) are indexed and read on demand. This distinction is correct but worth flagging as a design load-bearing point.

---

## 4. Storage architecture: blob store, resumable uploads, presigned URLs

### Blob storage patterns

**Production defaults** ([aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html](https://aws.amazon.com/AmazonS3/latest/userguide/using-presigned-url.html)):
- **Presigned URLs**: server generates a time-limited, operation-specific signed URL; client uploads directly to S3 (or S3-compatible store); server never touches file bytes. Avoids bottleneck and bandwidth waste; requires no server-side streaming.
- **Resumable uploads (tus protocol)**: client persists upload progress in browser local storage; resume across network failures without re-uploading. tus-js-client is a pure JavaScript library suitable for PWAs ([tus.io](https://tus.io/)); Cloudflare Stream and bunny.net Stream both support tus for video ([developers.cloudflare.com/stream/uploading-videos/resumable-uploads](https://developers.cloudflare.com/stream/uploading-videos/resumable-uploads/)).
- **Streaming through server**: client → server → S3. Simplest to code, slowest, and server becomes a bandwidth bottleneck (every byte in = every byte out). Acceptable only for small files or when presigned URLs are not feasible.

**Recommendation**: presigned URLs for single-file uploads, tus for resumable multi-file in a PWA. Neither requires decryption of Provider Keys on the blob-store side (only on jelly-fish's Go server, following ADR 0006).

### Content-type sniffing vs. file extension

**Best practice**: trust the file extension (`.pdf`, `.txt`, `.png`) sent by client, but validate the file's magic bytes (first N bytes of content) server-side. This prevents a malicious `.exe` disguised as `.pdf` and limits OCR/parser resource exhaustion — a zip bomb can have a `.pdf` extension.

---

## 5. Security: prompt injection, zip bombs, XSS, cross-origin serving

### Prompt injection via uploaded content

**Real-world pattern**: hidden (e.g. white-on-white) text embedded in a PDF has been used to manipulate an agent into exfiltrating data via a tool call, independently documented by security researcher Simon Willison ([x.com/simonw/status/1969111931152634010](https://x.com/simonw/status/1969111931152634010)). Prompt injection is OWASP's top-ranked LLM risk, LLM01:2025, precisely because there is no reliable input-side filter for it ([genai.owasp.org/llmrisk/llm01-prompt-injection](https://genai.owasp.org/llmrisk/llm01-prompt-injection/)).

**⚠ cross-spec**: approver.md Decision 2 (§5.4) limits Jev's input to the tool call, its `ToolDef` flags, and the session's own `user.message` text — never tool results, fetched/Connector content, or (by the same rule) inlined Upload content. So an injection payload inside an Upload can influence the main model's own text, but it cannot talk its way past the Approver: Jev never sees it, and any tool call the hijacked agent tries still goes through the normal rules → Jev → user path unchanged. Upload content is inherently untrusted, the same as fetched or Connector content; the main model's exposure to it is an accepted risk, since the user chose to attach the file. Keyword-pattern flagging ("ignore this prompt," "act as," …) is not a real mitigation — such filters are trivially bypassed (encoding, indirection, translation) and mostly give false confidence.

### Zip bombs and decompression attacks

**Attack**: a 42 KB `.zip` expands to petabytes; extraction exhausts memory/disk. **Prevention** ([mimecast.com/content/what-is-a-zip-bomb](https://www.mimecast.com/content/what-is-a-zip-bomb)): (a) refuse `.zip` and `.tar` archives as Uploads entirely, (b) if supporting them, unzip in an isolated container with resource limits (CPU, memory, nested-depth cap), (c) set server-side size caps (e.g. max 100 MB uncompressed, max 3 nesting levels).

**Recommendation**: reject archives in the base version. If deferred support is needed, decompress only in the Sandbox ([ADR 0003](../adr/0003-nothing-executes-on-the-host.md): nothing executes on the host), with explicit resource limits and a timeout.

### SVG and HTML XSS in previews

**Vulnerability** ([github.com/advisories/GHSA-7jp5-298q-jg98](https://github.com/advisories/GHSA-7jp5-298q-jg98)): serving an uploaded SVG as `Content-Type: image/svg+xml` allows embedded `<script>` tags to execute in the context of the main app origin, stealing auth tokens. **Prevention** ([svggenie.com/blog/svg-xss-sanitize-guide](https://www.svggenie.com/blog/svg-xss-sanitize-guide)): (a) convert SVGs to PNG/JPG on upload, (b) sanitize by removing script tags, or (c) serve previews from a separate, cookie-less origin with `Content-Disposition: attachment` to prevent inline execution.

**Recommendation**: serve all file previews (thumbnails, extracted images) from a separate origin scoped to the preview domain only. This leverages the browser's same-origin policy and prevents stolen auth tokens even if a malicious SVG or HTML file is rendered. Verified precedent: claude.ai serves every Artifact from a sandboxed `*.claudeusercontent.com` origin, separate from `claude.ai` itself ([code.claude.com/docs/en/artifacts](https://code.claude.com/docs/en/artifacts), "Allowlist the viewer domain"). (`artifactserver.com`, cited in an earlier draft, is a third-party self-hosted project's own site, not an independent authority on this pattern, and CLIs like Claude Code/Codex CLI don't serve Artifacts themselves — the web viewer does.)

---

## 6. Context-window cost and Compaction interaction

**When does content count?** Inline Upload base64 (Decision 11) counts toward context at send time. When the Upload is swapped to a stub note (Decision 11 projection rule after N=3 user messages), the stub itself is short (e.g. `[report.pdf, 42 pages, 3 MB. Removed from view; call read_upload("up_7") to read it]`) and costs only ~20 tokens instead of thousands.

**Compaction interaction** (context.md §5.3): a Tier 2 summary may reference earlier uploads by name or content. If those uploads are still inline in the historical context window being summarized, the summary must account for their token cost. The projection rule guarantees that after-swap Uploads are already stubbed before Compaction, so summaries never re-inline huge files.

**Design note**: jelly-fish's Compaction already handles this correctly via the projection rule — swapping to stubs *before* Compaction runs ensures the summarizer never sees the full inline content of old Uploads. No new mechanism needed.

---

## 7. Artifact versioning: precedent and design

**ChatGPT Canvas (2024–May 2026)**: supported version history (restore to earlier version via back button), but OpenAI removed Canvas from its GPT-5.5 chat models on May 28, 2026 (secondary source quoting OpenAI release notes), replacing it with inline code blocks and a full-screen editor ([ai-toolbox.co/chatgpt-management-and-productivity/how-to-use-chatgpt-canvas-guide-2026](https://www.ai-toolbox.co/chatgpt-management-and-productivity/how-to-use-chatgpt-canvas-guide-2026)).

**Claude.ai Artifacts (2024–2026)**: shipped version history ("Version history" menu, list all published versions), but a September 2026 user bug report says the option disappeared; it's labelled a regression and the docs still describe versions, so **Unverified** as a deliberate removal ([github.com/anthropics/claude-code/issues/96718](https://github.com/anthropics/claude-code/issues/96718)). No programmatic API to list or retrieve prior versions ([github.com/anthropic-ai/anthropic-sdk-python/issues/79901](https://github.com/anthropic-ai/anthropic-sdk-python/issues/79901)).

**jelly-fish's design** (TODO.md, Decisions from 2026-09-27): `save_artifact(name, content?, path?)` writes a blob + `artifact.saved` event; calling `save_artifact` with the same name again creates a new *version* (v1, v2, …, latest on top), not a new artifact. This is simpler than full revision history and aligns with the industry trend of vendors *removing* version complexity. No open-ended grilling item here — the design is sound and matches the field.

---

## 8. Project files vs. Uploads

**Distinction** (provider-gateway Decisions 22–23):
- **Uploads**: attached by the user to a Session, inline until swapped to stub (Decision 11), appear in session Files panel (Uploads tab), deleted via session Files panel.
- **Project files**: managed in project settings, indexed in the prompt (name, id, size only), read via `read_upload` tool, shared across all sessions in the Project.

Both use the same `read_upload` tool and `BlobRef` storage. The distinction is scope (per-session vs. per-project) and discoverability (Files tab vs. indexed list in the prompt).

---

## 9. Open questions for grilling

1. **What should jelly-fish's per-Upload size limit be, and what happens when a file that fits it exceeds the *current* Provider's inline limit?** The strictest binding number is Anthropic's 32 MB total-request cap (§2) — and that cap covers the *whole* request (system, history, other Uploads), not just the one file, so setting jelly-fish's limit anywhere near 32 MB leaves no room for anything else in the prompt. Proposed: cap per-Upload at 20 MB. That clears OpenAI's 50 MB file cap and Gemini's 100 MB inline cap with margin, and leaves most of Anthropic's 32 MB budget free for history and system content. The remaining failure mode is a `/model` switch mid-session (provider-gateway.md §5.1, agent-loop.md Decision 6) onto Anthropic, after the Upload was attached under a more permissive Provider, where the accumulated request (Upload + history) now exceeds 32 MB: map that to the existing `too_large` error class (provider-gateway.md §9 Decision 9) — "File too large for the selected Provider" → Remove file or switch back.

2. **Should the Upload flow support archives (`.zip`, `.tar`)?** No surveyed tool encourages it; zip bombs are a real threat. Proposed: reject archives in the base version; if deferred support lands, decompress only in the Sandbox with strict resource limits, never on the server itself.

3. **Artifact retention and deletion: does the user delete Artifacts, and how long do versions live?** provider-gateway.md Decision 22 only covers Uploads (`upload.deleted` event, immediate blob removal, no grace period); TODO.md's Artifacts note doesn't say. event-log.md's own cleanup model is hard-delete only — an async job driven by the `deletions` table removes blobs (and Artifacts) once triggered, with no soft-delete/grace-period concept anywhere in the ground truth docs. Proposed: treat Artifacts the same as Uploads — hard-delete on explicit user request (e.g. from the Artifacts tab), all versions of that Artifact removed together, no auto-purge after a fixed period.

4. **How should `read_upload` handle large PDFs?** The `pages` argument (Decision 23) allows the agent to request a page range, e.g., `read_upload("pdf_123", pages="1-5")` for the first five pages. Should page extraction happen at request time or should the server pre-split PDFs? Proposed: extract at request time in a tool-call handler; this keeps Upload storage simple and avoids the need to index/store page boundaries for every PDF (overhead for jelly-fish, not the Provider).

5. **Should Uploads have their own per-user storage Quota, separate from session token/cost Budget?** CONTEXT.md defines Quota as "a per-user limit on platform resources (sandboxes, compute, storage), set by an admin," and ADR 0006 says Quotas "exist from day one" because the platform carries real spend — so any number here is a placeholder, not a decision this doc can make. Proposed: yes, add a per-user (and optionally per-project) Upload storage Quota, metered via `usage.recorded` with `kind=upload_storage`, `unit=megabytes`, same pattern as sandbox compute; the actual GB figures are left for an admin to configure, not fixed by this research.

6. **Should file previews (thumbnails, extracted images) be generated server-side or deferred to the client?** Generating server-side (e.g., with ImageMagick or a library like pdfjs for PDF thumbnails) costs CPU. Deferring to the client (send the raw file, let the browser render it) saves server cost but risks XSS if the file is malicious. Proposed: generate thumbnails server-side for security (sanitize/convert to PNG before serving), store them as a separate blob with a short TTL (e.g., 7 days), and serve from a separate preview origin (§5) with `Content-Disposition: attachment` to prevent inline XSS.

7. **What file types should jelly-fish accept, and should DOCX/XLSX/PPTX be converted server-side or rejected until the Sandbox exists?** §2 confirms DOCX/XLSX/PPTX aren't native inline input for every Provider — Anthropic's Messages API rejects them outright (PDF-only document block), OpenAI's Responses API accepts them natively, Gemini is **Unverified** beyond its MIME-type list. ADR 0003 (nothing executes on the host) means a server-side converter would be parsing untrusted binary input outside any isolation boundary. Proposed: base accepted set = PDF, images, and text-like files (txt/md/csv/json/code) — inline-native for at least two of three Providers with no conversion step; reject DOCX/XLSX/PPTX in the base version, and only add server-side conversion once the Sandbox exists, so untrusted-format parsing happens inside it rather than on the host.

8. **Is a presigned direct-to-blob upload enough for the base version, with tus/resumable deferred?** The blob store itself is only partly designed: event-log.md §3 defines object-storage blobs keyed `ws/{workspace_id}/sess/{session_id}/blobs/{sha256}`, but that's the path for event-payload parts a Worker writes (tool results, screenshots), not a browser-facing user Upload endpoint — that transport isn't designed yet (TODO.md: "Still to design: upload flow"). Proposed: yes — a presigned URL (server checks the per-Upload size cap from Q1, then issues a short-lived signed URL; client uploads directly to object storage) for the base version; defer tus/resumable, since jelly-fish's Upload sizes (tens of MB, not multi-GB video) don't need chunked resumability, and it adds real client/server complexity the base version doesn't need.
