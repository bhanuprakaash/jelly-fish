# Usage Metering and Quotas: Spec

Status: spec, 2026-09-27. Terms follow [`CONTEXT.md`](../../CONTEXT.md): **Usage** is measured consumption, recorded per Session (never cost/consumption/billing); a **Budget** is a per-Session limit; a **Quota** is a per-User limit on Platform Services and storage, set by an admin; a **Platform Service** is one the platform pays for (Jev, web search); **Jev** is shown only as "auto mode". Builds on ADR [0006](../adr/0006-byok-llm-platform-metered-services.md) (BYOK for LLMs, platform-metered services, Quotas from day one), [event-log.md](event-log.md) §3 (`usage` table), §4 (`usage.recorded`), §5.9 (Budget), §5.13 (hard delete), [provider-gateway.md](provider-gateway.md) §5.3 and Decision 12 (normalized `Usage`, one row per token class, `cost_micros` fixed at record time), [approver.md](approver.md) Decisions 4, 22 (Jev Quota → ask, banner), [uploads-artifacts.md](uploads-artifacts.md) §5.9 and Decision 13 (1 GB storage Quota), [auth-keys.md](auth-keys.md) Decision 9 (admins set Quotas). No research doc: grilled straight from the existing specs.

## 1. Summary

- Usage is for **monitoring**: a User can compare our numbers with their Provider's own usage page, so we show tokens by Provider → model → token class, with an estimated $ (D5, D8).
- `usage` rows gain `provider` and `model` (D5).
- Quotas are **counts per service per calendar month (UTC)**: auto-mode checks 3,000, web searches 300 (inactive in the base version: web search is the Provider's built-in one on the user's key, provider-gateway.md D6a). Storage stays 1 GB in bytes, no reset (D0, D1).
- Every request sent to a Platform Service counts as 1, even on timeout or error (D2).
- No counter table: the Quota check is a `SUM` over this month's `usage` rows; a soft limit (D3).
- At the limit: Jev falls back to ask with a dated banner; search returns a tool error (D4).
- Hard delete keeps usage in its original month (D6).
- Only LLM rows feed a Session's Budget; platform rows carry the platform's cost for the admin view only (D7).
- Screens: Usage dashboard, `/cost`, Admin → Usage with per-User Quota form (D8–D10).

## 2. Scope

**In the base version**
- `usage.provider`, `usage.model`; month-preserving hard-delete collapse; LLM-only Budget projection.
- `user_quotas` table, config defaults, `Quotas.Check`.
- Jev and web-search Quota enforcement hooks.
- Usage dashboard, `/cost`, Admin → Usage.
- Platform per-call prices in the catalog.

**Out (deferred)**
- Sandbox Quotas (concurrent Sandboxes, compute hours, CPU-s, GB-s, egress): no Sandbox in the base version. `kind` values `sandbox_*` stay reserved.
- The `search` Quota and `kind = search` rows: designed here, active only once our own platform search ships (ADR 0006 amendment). In the base version Provider built-in searches are `kind = llm`, `unit = web_search_requests`, on the user's key, with no Quota.
- 80% warnings and Quota notifications (Notifications spec, if ever).
- Billing, invoices, payments.
- Importing real spend from Provider billing APIs.
- A cached counter table (only if the `SUM` shows up in profiling).
- Per-User time zones for the reset (reset is 00:00 UTC on the 1st).

## 3. Data model

Changes to `usage` (event-log.md §3):

```sql
ALTER TABLE usage
  ADD COLUMN provider text NOT NULL,   -- anthropic | openai | gemini | jev | brave | tavily
  ADD COLUMN model    text;            -- LLM rows: catalog model id; NULL for platform rows
CREATE INDEX usage_user_kind_time ON usage (user_id, kind, created_at);
```

`kind` / `unit` values in the base version:

| kind | provider | unit | quantity | cost_micros |
|---|---|---|---|---|
| `llm` | anthropic / openai / gemini | `input_tokens`, `cache_read_tokens`, `cache_write_5m_tokens`, `cache_write_1h_tokens`, `output_tokens`, `reasoning_tokens`, server-tool units (provider-gateway.md §5.3) | tokens | estimate from catalog list prices (User's own spend) |
| `jev` | jev | `checks` | 1 | platform's cost per check (catalog) |
| `search` | brave / tavily | `searches` | 1 | platform's cost per search (catalog) |

New table:

```sql
CREATE TABLE user_quotas (                 -- admin overrides; absent row = config default
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  kind          text NOT NULL CHECK (kind IN ('jev','search','storage_bytes')),
  monthly_limit bigint,                    -- NULL = unlimited; storage_bytes: total, not monthly
  updated_by    uuid NOT NULL REFERENCES users(id),
  updated_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (user_id, kind)
);
```

Config defaults: `JF_QUOTA_JEV_MONTHLY=3000`, `JF_QUOTA_SEARCH_MONTHLY=300`, `JF_QUOTA_STORAGE_BYTES=1073741824`.

Catalog (`internal/llm/catalog`, provider-gateway.md §3.4) gains per-call platform prices: `jev.check`, `brave.search`, `tavily.search`.

## 4. Contracts

### 4.1 Quotas

```go
type QuotaKind string // "jev" | "search" | "storage_bytes"

type QuotaStatus struct {
    Used, Limit int64      // Limit < 0 = unlimited
    ResetAt     time.Time  // first of next month 00:00 UTC; zero for storage
}

type Quotas interface {
    Check(ctx context.Context, ts TenantScope, k QuotaKind) (ok bool, st QuotaStatus, err error)
}
```

`ok = Limit < 0 || Used < Limit`. On a DB error `Check` returns `ok = false` (fail closed, like approver.md Decision 18).

### 4.2 HTTP

| Method, path | Result |
|---|---|
| `GET /usage?from=&to=&project_id=&session_id=` | LLM breakdown + platform + storage (§4.3) |
| `GET /sessions/{id}/cost` | `/cost` data (§5.5) |
| `GET /admin/usage?month=2026-09` | one row per User (§5.6) |
| `PUT /admin/users/{id}/quotas/{kind}` | `{mode: "default" \| "limit" \| "unlimited", limit?}` |

### 4.3 `GET /usage` response

```json
{
  "from": "2026-09-01", "to": "2026-09-27", "tz": "UTC",
  "llm": [
    {"provider": "anthropic", "model": "claude-sonnet-5", "classes": [
      {"unit": "input_tokens", "tokens": 812345, "cost_micros": 2437035},
      {"unit": "cache_read_tokens", "tokens": 4021000, "cost_micros": 1206300},
      {"unit": "output_tokens", "tokens": 98012, "cost_micros": 1470180}
    ], "cost_micros": 5113515}
  ],
  "platform": {
    "jev":    {"used": 2950, "limit": 3000, "reset_at": "2026-10-01T00:00:00Z"},
    "search": {"used": 41,   "limit": 300,  "reset_at": "2026-10-01T00:00:00Z"}
  },
  "storage": {"used_bytes": 312000000, "limit_bytes": 1073741824},
  "by_day": [{"day": "2026-09-01", "provider": "anthropic", "model": "claude-sonnet-5", "unit": "input_tokens", "tokens": 30211}]
}
```

Platform `cost_micros` is never returned to non-admins.

## 5. Algorithms and flows

### 5.1 Recording (extends provider-gateway.md §5.3)

- LLM: unchanged, plus `provider` and `model` on each `usage.recorded` and `usage` row.
- Jev: the Worker appends `usage.recorded{kind: jev, provider: jev, unit: checks, quantity: 1}` for every Jev request **sent**, including ones that time out or error (D2), fenced, in the same tx as the approver's outcome event.
- Search: same shape, `kind: search`, written by the web-search tool (Web search spec).

### 5.2 Quota check (D3)

```sql
SELECT coalesce(sum(quantity), 0) FROM usage
WHERE user_id = $1 AND kind = $2
  AND created_at >= date_trunc('month', now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
```

- Limit: `user_quotas` row if present (NULL = unlimited), else the config default.
- Called before each Jev request and each search; never cached.
- Soft limit: calls already in flight may take a User 1–2 over. Accepted.
- Storage: the live-bytes query of uploads-artifacts.md §5.9 against `storage_bytes`.

### 5.3 At the limit (D4)

- **Jev**: the Approver treats the call as `jev_quota_exhausted` → ask (approver.md Decision 4). Banner: "Auto mode can't check actions right now, so I'll ask you before each one until Oct 1." The date is `ResetAt` shown in the User's local date.
- **Search**: the tool returns `is_error` "Web search limit reached until Oct 1." to the model, and the chat shows one inline line with the same text. No usage row (nothing was sent).
- **Storage**: unchanged (uploads-artifacts.md D13).

### 5.4 Budget projection (D7; changes event-log.md §4 `project`)

`project(usage.recorded)` always inserts the `usage` row. It adds to `sessions.tokens_used` and `sessions.cost_micros` **only when `kind = 'llm'`**; a `web_search_requests` row adds its cost but not its count to `tokens_used`. The child roll-up in `FinishChild` (event-log.md §5.10) therefore also carries LLM totals only.

### 5.5 `/cost` (D9)

For one Session: LLM tokens by model and class with ≈ $; turns this message; auto-mode checks and searches in this Session; one rolled-up line per Child Session ("research-agent: 120k tokens, ≈ $0.40"); Budget left per dimension ("$1.39 of $2 left", "tokens 310k of 500k"). Platform $ never shown.

### 5.6 Admin → Usage (D10)

For the chosen month, one row per User: auto-mode checks used / limit, searches used / limit, storage used / limit, platform $ (sum of `jev` + `search` `cost_micros`). Users' LLM $ is not shown. Each row opens the Quota form: per kind, "Default (3,000)", a number, or "Unlimited". Admins are under Quotas like anyone else.

### 5.7 Hard delete (D6; changes event-log.md §5.13)

The collapse groups by month and keeps it:

```sql
INSERT INTO usage (workspace_id, project_id, user_id, kind, unit, provider, model, quantity, cost_micros, created_at)
SELECT workspace_id, project_id, user_id, kind, unit, provider, model, SUM(quantity), SUM(cost_micros),
       date_trunc('month', created_at AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'
FROM usage WHERE session_id = ANY($1)
GROUP BY workspace_id, project_id, user_id, kind, unit, provider, model,
         date_trunc('month', created_at AT TIME ZONE 'UTC');
```

The dashboard loses the per-day split of a deleted chat (its totals land on the 1st of each month), but monthly totals and Quotas stay right.

## 6. Rules and invariants

- LLM usage is the User's own spend: shown to them as an estimate, never to admins, never Quota-limited (the Budget is their guard).
- Platform usage is the platform's spend: counted against Quotas, its $ visible to admins only.
- `cost_micros` is fixed at record time; never recalculated (provider-gateway.md D12).
- Every request sent to a Platform Service writes exactly one `usage` row.
- Only `kind = 'llm'` feeds Budget counters.
- Days and months are UTC everywhere in Usage.
- A failed Quota check never allows (fail closed).

## 7. Events

No new event types. `usage.recorded` gains `provider` and `model` (event-log.md §4). `usage.recorded` is never sent on SSE (streaming.md §4.3).

## 8. UI

- **Usage page**: period picker (default this month; dates are UTC, said on screen). Per Provider, a table of models × token classes with tokens and ≈ $, labels matching that Provider's own usage page (checked against each console during build). A daily chart. Filters: Project, chat. Platform box: "Auto mode checks 2,950 / 3,000 · resets Oct 1", "Web searches 41 / 300", "Storage 312 MB / 1 GB". Memory tidy rows show as "Memory tidy". Footer: "$ is estimated from list prices. Your Provider's console is the source of truth."
- **`/cost`**: a card in the chat with §5.5.
- **Auto-mode banner**: dated, as §5.3.
- **Search limit line**: inline, as §5.3.
- **Admin → Usage**: §5.6 table + Quota form.

## 9. Decisions

All accepted 2026-09-27 (grilling Q0–Q10).

0. **Quotas are counts per service per calendar month (UTC)**; storage is bytes with no reset. (Q0)
1. **Defaults**: auto mode 3,000 checks / month, web search 300 / month, storage 1 GB; config-set; admin overrides per User incl. unlimited; admins are not exempt. (Q1)
2. **Every request sent counts as 1**, including timeouts and errors. (Q2)
3. **No counter table**: `SUM` over this month's `usage` rows before each call; soft limit. (Q3)
4. **At the limit**: Jev → ask with a dated banner; search → tool error + inline line; no 80% warning. (Q4)
5. **`usage` gains `provider` and `model`**; LLM `unit` = token class (existing names from provider-gateway.md §5.3). (Q5)
6. **Hard-delete collapse keeps the month** (`created_at` = first of that month, UTC). (Q6)
7. **Only `kind = llm` feeds Budget counters**; platform rows carry the platform's cost for admins. (Q7)
8. **Usage dashboard is for comparing with Provider consoles**: Provider → model → token class, ≈ $, UTC days, Project/chat filters, platform Quota bars. (Q8, follow-up)
9. **`/cost`**: this chat's LLM tokens and ≈ $, turns, auto-mode checks, searches, child roll-ups, Budget left. (Q9)
10. **Admin → Usage**: per-User platform usage and platform $, Quota form; no LLM $ of Users. (Q10)

## 10. Edge cases

- **Two Jev calls in flight at 2,999 / 3,000**: both run; the User ends at 3,001. Next call asks.
- **Admin lowers a Quota below current use**: the next check fails; nothing is undone.
- **Month rolls over mid-session**: the next check counts from the new month; the banner disappears on the next Jev-eligible call that succeeds.
- **Deleting an August chat in September**: August totals stay in August (D6).
- **Catalog has no price for a model** (new model): `cost_micros = 0` and the dashboard shows "price unknown" for that model; tokens still count.
- **Price fixed in the catalog later**: past rows keep their old `cost_micros` (provider-gateway.md D12).
- **User brings their own search key** (Web search spec): those searches are theirs, not platform usage; not Quota-limited. Recorded with `kind = search` only if that spec says so.
- **Jev disabled because the mode is `ask` or `full-auto`**: no Jev requests, no rows.
- **Child Sessions**: their rows carry the same `user_id`, so they count toward the User's Quotas; their LLM totals roll up to the parent Budget.
- **Quota check DB error**: fail closed; Jev → ask, search → tool error.

## 11. Acceptance criteria

- An LLM turn writes `usage` rows with `provider`, `model` and a token-class `unit`, one per non-zero class.
- A Jev request that times out writes one `kind = jev` row with `quantity = 1`.
- With a 3-check Quota, the 4th Jev-eligible call becomes an ask with `reason: jev_quota_exhausted`, and the banner shows the 1st of next month.
- With a 2-search Quota, the 3rd search returns the limit tool error and writes no `usage` row.
- `user_quotas` unlimited → no call is ever refused; a row with a number overrides the config default; deleting the row restores the default.
- A Session's `tokens_used` / `cost_micros` don't change when `jev` or `search` rows are recorded.
- Hard-deleting a chat whose usage spans Aug and Sep leaves one collapsed row per (…, month) with `created_at` = Aug 1 / Sep 1 UTC; September's Jev count is unchanged by deleting an August-only chat.
- `GET /usage` totals for a test month equal the sum of the fixture's `usage` rows, grouped by provider, model and unit, with days cut at UTC midnight.
- `GET /usage` for a non-admin never includes platform `cost_micros`; `GET /admin/usage` is `404` for non-admins.
- `/cost` for a parent with one finished child shows the child's rolled-up line and Budget left that matches `sessions` counters.
- A model with no catalog price shows "price unknown" and `cost_micros = 0`.

## 12. Open gaps

None. Deferred work is listed in §2 Out.

## 13. Research

None; decisions come from the specs linked in the header and grilling on 2026-09-27.
