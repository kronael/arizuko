---
status: unshipped
---

# 29 — rate limits and credits

Usage tracking + per-group rate limits. No billing, no token counting
(Claude Code CLI doesn't expose token usage). We meter what's
observable: container wall-clock time and invocation count.

## What to meter

| Metric          | Source                           |
| --------------- | -------------------------------- |
| `invocations`   | `container.Run` call count       |
| `container_sec` | `time.Since(start)` in runner.go |

## Schema

Migration `0027-usage-log.sql`:

```sql
CREATE TABLE IF NOT EXISTS usage_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  folder TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  sender TEXT NOT NULL,
  started_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  result TEXT NOT NULL,  -- ok | error | timeout
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_usage_folder_created
  ON usage_log(folder, created_at);
```

Migration `0028-usage-limits.sql`:

```sql
CREATE TABLE IF NOT EXISTS usage_limits (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  scope TEXT NOT NULL,        -- folder glob: "*", "pub/*", "pub/acme"
  metric TEXT NOT NULL,       -- "invocations" | "container_sec"
  period TEXT NOT NULL,       -- "day" | "hour"
  max_value INTEGER NOT NULL,
  created_at TEXT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_usage_limits_scope_metric
  ON usage_limits(scope, metric, period);
```

`scope` uses the same glob matching as `user_groups` (grants package).

## Enforcement

Pre-check in `processSenderBatch`, before `runAgentWithOpts`:

```
usage := store.UsageSince(folder, periodStart)
limit := store.MatchingLimit(folder, metric)
if limit > 0 && usage >= limit {
    send "Rate limit reached, try again later."
    return true  // consumed, not retried
}
```

Soft reject: message acknowledged (cursor advances), no container spawns.
Enforcement stays in gateway where all state is available; the container
has no DB access.

## Recording

After `container.Run` returns in `runAgentWithOpts`:

```go
g.store.RecordUsage(group.Folder, chatJid, sender, elapsed, result)
```

Add `DurationMs int64` to `container.Output`; set in `runner.Run`.

## Store methods

- `RecordUsage(folder, chatJid, sender string, dur time.Duration, result string)`
- `UsageSince(folder, metric, since string) int64`
- `UsageSummary(folder string, days int) []UsageRow`
- `SetLimit(scope, metric, period string, max int)` / `DeleteLimit(...)`
- `MatchingLimit(folder, metric, period string) int`

## Limit resolution

Most specific scope wins (longest literal prefix before any glob character).
Ties: lowest `max_value` wins. Matches grants glob precedence.

## CLI

```
arizuko group <instance> limit set <scope> <metric> <period> <max>
arizuko group <instance> limit rm <scope> <metric> <period>
arizuko group <instance> limit ls
arizuko group <instance> usage <folder> [--days=7]
```

## Dashboard

New `/usage` page in dashd: per-group invocation count and container-seconds
for last 7/30 days, limit status (current / max, red when >80%), grouped
by world. Read-only HTMX, same pattern as existing dashd pages.

## Config

Two env vars for global defaults:

- `RATE_LIMIT_INVOCATIONS_PER_DAY` — default: 0 (unlimited)
- `RATE_LIMIT_CONTAINER_SEC_PER_DAY` — default: 0 (unlimited)

These create implicit `*` scope limits if set; explicit `usage_limits`
rows override them.
