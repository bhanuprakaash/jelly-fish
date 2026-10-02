---
name: start-issue
description: "Start a jelly-fish issue: pick or load it with its specs, branch, plan the seams, stop for OK, then build test-first and review. Usage: /start-issue [number | issue URL]"
disable-model-invocation: true
---

# Start an issue

Takes an issue number or URL in the arguments, or nothing. Ends with reviewed, green, **uncommitted** work on a feature branch, ready for `/close-issue <n>`.

**DB URL**, for every step that needs Postgres: prefix the command with `DATABASE_URL=$(zsh -ic 'jellyfish_db' 2>/dev/null | tail -1)` (tests: `TEST_DATABASE_URL=…`). The function lives in `~/.zshrc` and is pre-authorized; take it from the shell rather than asking. Keep the value out of output and files. Without `TEST_DATABASE_URL` the DB tests skip silently.

## 1. Pick the issue

- Argument given: use it.
- None: `gh issue list --repo bhanuprakaash/jelly-fish --state open`, then for candidates `gh issue view <n>`. An issue is **ready** when every issue in its "Blocked by" is closed. Recommend one (prefer the one that unblocks the most), list the other ready ones, and wait for the user's pick.

Also look for a handoff (`ls -t $TMPDIR/jelly-fish-handoff-*.md`) and for follow-ups left in the last closed sibling's "Done" section or closing comment. Note any that bear on this issue.

Done when: one issue number is chosen.

## 2. Load the context

Read, in full: the issue; its parent's body (the "Decisions" section binds every slice); each spec section the issue or parent links (e.g. `docs/design/auth-keys.md §5.5`); `docs/style.md`; `CONTEXT.md` for domain terms. Then copy the issue's acceptance criteria verbatim; that list is the **checklist**.

Done when: the checklist is written out, and every spec link has been opened.

## 3. Set up

- `git status` must be clean and `main` current with `origin/main` (`git fetch`, fast-forward). If not, stop and ask.
- `git checkout -b feat/<short-outcome-slug>` from `main`.
- Check the forwards: `lsof -iTCP -sTCP:LISTEN -P | grep -E ':(5433|8025|8080)\b'`. Start a missing one with `--context jelly-fish` and note it. kubectl's current context may be a remote cluster; pass `--context jelly-fish` on every call.

Done when: on the new branch, and `:5433` answers.

## 4. Scout and plan

Read the code each checklist line touches. Then write the plan:

- the **seams** to test at (store, HTTP handler, stream, UI), each with the checklist lines it proves;
- routes, tables or migrations, config, and UI changes;
- the follow-ups from step 1 that this issue absorbs, and those it leaves;
- a terse list of unresolved questions.

Then **stop** and wait for the user's OK. Their answers change the plan; restate only what changed.

## 5. Build test-first

Run the `mattpocock-skills:tdd` skill at the agreed seams: one failing test, then the code that turns it green, seam by seam. While building, run `go build ./... && go vet ./...` and the single test package (`go test ./internal/<pkg>/`) often; for `web/`, `pnpm lint && pnpm exec tsc -b` in `web/`.

At the end, the full gate: `make lint test` with `TEST_DATABASE_URL` set. If the diff touches `internal/api`, `internal/health`, `internal/worker`, `internal/migrate`, `cmd/jelly-fish` or `web/`, run the `verify-jelly-fish` skill and drive every checklist line live where it can be. A `pnpm build` rewrites the tracked `web/dist/index.html`; restore it with `git checkout web/dist/index.html`.

Done when: every checklist line has a passing test or live proof, and the full gate is green.

## 6. Review

Run the `mattpocock-skills:code-review` skill. The spec is the issue; the diff is the working tree (`git add -N .` first so new files show, then `git diff HEAD`). Fix findings that are real defects or unmet spec lines, rerun the gate, and keep the rest as a list for the user.

Done when: the gate is green after the fixes.

## 7. Hand off

Leave the work uncommitted on the branch. Report tersely: the checklist with its evidence, the review findings left unfixed, anything started in step 3, new follow-ups, and the next step: `/close-issue <n>`.
