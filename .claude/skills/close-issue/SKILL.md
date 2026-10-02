---
name: close-issue
description: "Finish a jelly-fish issue: prove its acceptance criteria locally, redeploy the cluster for a hand test, then on approval commit, push to main, tick the body and close it. Usage: /close-issue <number>"
disable-model-invocation: true
---

# Close an issue

Takes the issue number in the arguments. The work is already implemented in the working tree or on a local branch; this skill proves it, ships it and closes the ticket.

## 1. Pull the criteria

`gh issue view <n> --repo bhanuprakaash/jelly-fish`. Copy every `- [ ]` line under "Acceptance criteria" verbatim; that list is the **checklist** for every later step.

Done when: the checklist is written out in your reply.

## 2. Prove each criterion

- `TEST_DATABASE_URL=$(zsh -ic 'jellyfish_db' 2>/dev/null | tail -1) make lint test`. Without `TEST_DATABASE_URL` the DB tests skip and prove nothing.
- If the diff touches `internal/api`, `internal/health`, `internal/worker`, `internal/migrate`, `cmd/jelly-fish` or `web/`: run the `verify-jelly-fish` skill, and drive each criterion live where it can be.
- `cd web && pnpm lint && pnpm exec tsc -b` if `web/` changed. A `pnpm build` rewrites the tracked `web/dist/index.html`; restore it with `git checkout web/dist/index.html`.

Map every checklist line to its evidence: a test name, or a live command with its status and output. A line proven only indirectly (a unit test with a fake, or a sibling code path) is marked **partial** with the reason. Never run a live action that would end the user's own sessions or data (e.g. `logout-all` on `admin@jelly-fish.local`); use a throwaway path and say so.

Done when: every checklist line has evidence or an explicit partial/missing mark, and lint and tests are green.

## 3. Redeploy for a hand test

Only against kubectl context `jelly-fish`; pass `--context jelly-fish` on every call.

```
docker build --build-arg TAGS=dev -t localhost:5001/jelly-fish:latest . \
  && docker push localhost:5001/jelly-fish:latest \
  && kubectl --context jelly-fish -n jelly-fish rollout restart deploy/api deploy/worker \
  && kubectl --context jelly-fish -n jelly-fish rollout status deploy/api --timeout=120s \
  && kubectl --context jelly-fish -n jelly-fish rollout status deploy/worker --timeout=120s
```

A rollout kills the pod behind an existing `:8080` port-forward. Kill that forward by its PID (`lsof -iTCP:8080 -sTCP:LISTEN`) and restart it in the background: `kubectl --context jelly-fish -n jelly-fish port-forward svc/api 8080:8080`. Confirm the new build answers with a curl that only it passes (a new route's status).

Done when: the new build answers on `http://localhost:8080`.

## 4. Stop for approval

Show: the checklist with evidence (table), what is partial, the parent lines step 6 will tick or split (before → after), and hand-test steps for `http://localhost:8080` (Mailpit at `http://localhost:8025`; login codes are capped at 3 per email per 15 min). Then **stop** and wait.

Invoking this skill approves nothing. Only an explicit "go" / "push" in reply authorizes steps 5–6, for this issue only.

## 5. Commit and push

- Commit any uncommitted work on the current branch: one terse conventional subject (`feat:`/`fix:`/`chore:`), no attribution of any kind.
- `git fetch origin`; `main` must fast-forward. If on a feature branch: `git checkout main && git merge --ff-only <branch>`. If it won't fast-forward, stop and ask.
- `git push origin main`.

Done when: `git status -sb` shows `main...origin/main` with nothing ahead.

## 6. Close the ticket

- Fetch the body to a temp file, tick every proven line (`- [ ]` → `- [x]`; macOS `sed -i ''`). Leave a partial line unticked unless the user accepted it.
- Add a `## Done` section above `## Blocked by`: the commit SHA and only the decisions a reader of the code would not guess.
- `gh issue edit <n> --body-file …`, then `gh issue close <n> --comment "Done in <sha>. <one line of what shipped>."` Delete the temp file.
- Tick the parent (the issue under `## Parent`): its "Acceptance criteria" is the source the sub-issue's criteria were cut from. For each parent `- [ ]` line, match it against the sub-issue's ticked lines:
  - fully delivered → `- [x]`;
  - partly delivered → split it in place into a `- [x]` line for the delivered part and a `- [ ]` line for the rest, each a self-contained criterion in the parent's wording;
  - not touched → leave it.

  Edit the parent body with `gh issue edit <parent> --body-file …`; leave the parent open.

Done when: `gh issue view <n> --json state` is `CLOSED`, its body shows the ticks, and every parent line this issue delivered is ticked or split.

## 7. Report

Terse: SHA pushed, issue closed, parent lines ticked or split, and every open issue whose "Blocked by" list is now fully closed (check the parent's sub-issues).
