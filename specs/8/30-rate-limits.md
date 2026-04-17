---
status: draft
---

# 29 — rate limits and credits

## Problem

Arizuko has no usage tracking. An operator cannot answer: how much did
group X cost this month? A runaway agent or abusive user can burn
unlimited Claude API credits. The only existing controls are global
concurrency (`MAX_CONCURRENT_CONTAINERS`) and per-group circuit breaker
(3 consecutive failures). Neither is cost-aware.

Self-hosted operators need: (1) visibility into usage per group,
(2) configurable rate limits to cap runaway spend, (3) optional credit
budgets. They do NOT need SaaS billing, invoicing, or payment processing.

## What to meter

Three dimensions, all derivable from container runs:

| Metric             | Source                                | Cost signal |
| ------------------ | ------------------------------------- | ----------- |
| **invocations**    | container.Run call count              | low         |
| **container-sec**  | `time.Since(start)` in runner.go      | medium      |
| **session result** | session_log.result (ok/error/timeout) | debugging   |

Token counting is out of scope. Claude API token usage is not surfaced
by the Claude Code CLI subprocess. If the operator wants token-level
tracking they should use Anthropic's usage dashboard or API key billing.
We meter what we can observe: container wall-clock time and invocation
count.

## Schema

One new table. Migration `0027-usage-log.sql`:

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

One new table for limits. Migration `0028-usage-limits.sql`:

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
`*` = all groups, `pub/*` = all onboarded groups, `pub/acme` = exact.

## Enforcement point

**Gateway pre-check, before container.Run.** In `processSenderBatch`,
after resolving the group and before calling `runAgentWithOpts`:

```
usage := store.UsageSince(folder, periodStart)
limit := store.MatchingLimit(folder, metric)
if limit > 0 && usage >= limit {
    send "Rate limit reached, try again later."
    return true  // consumed, not retried
}
```

This is a soft reject — the message is acknowledged (cursor advances)
but no container spawns. The user gets an immediate reply. This avoids
wasting queue slots or container resources.

**NOT in the container.** The container has no DB access and no
awareness of limits. Enforcement belongs in the gateway where all
state is available.

## Recording usage

After `container.Run` returns in `runAgentWithOpts`, record:

```go
g.store.RecordUsage(group.Folder, chatJid, sender, elapsed, result)
```

This is the same place `EndSession` is called (line ~783 in gateway.go).
`elapsed` comes from `time.Since(start)` — already computed in
`container/runner.go` but needs to be returned in `container.Output`.

Add `DurationMs int64` to `container.Output`. Set it in `runner.Run`
from `elapsed`.

## Querying usage

New store methods:

- `RecordUsage(folder, chatJid, sender string, dur time.Duration, result string)`
- `UsageSince(folder, metric, since string) int64` — sum of invocations
  or container_sec for a folder since a timestamp
- `UsageSummary(folder string, days int) []UsageRow` — for dashd display
- `SetLimit(scope, metric, period string, max int)` / `DeleteLimit(...)`
- `MatchingLimit(folder, metric, period string) int` — returns the most
  specific matching limit (exact folder > glob > wildcard)

## Limit resolution

Multiple `usage_limits` rows may match a folder. Resolution: most
specific scope wins (longest literal prefix before any glob character).
Ties: lowest `max_value` wins (conservative). This matches the grants
glob precedence pattern.

## Interaction with grants

Limits are NOT grant parameters. They are a separate orthogonal system:

- **Grants** answer: "is this agent allowed to do X?"
- **Limits** answer: "has this group exhausted its budget?"

No new grant actions needed. Limit management is operator-only (dashd
or CLI). Agents cannot modify their own limits.

## CLI

```
arizuko group <instance> limit set <scope> <metric> <period> <max>
arizuko group <instance> limit rm <scope> <metric> <period>
arizuko group <instance> limit ls
arizuko group <instance> usage <folder> [--days=7]
```

## Dashboard (dashd)

New page `/usage` showing:

- Per-group invocation count and container-seconds for last 7/30 days
- Limit status (current usage / max, red when >80%)
- Grouped by world (top-level folder)

This is a read-only HTMX page, same pattern as existing dashd pages.

## Config

Two env vars for global defaults (no per-group env):

- `RATE_LIMIT_INVOCATIONS_PER_DAY` — default: 0 (unlimited)
- `RATE_LIMIT_CONTAINER_SEC_PER_DAY` — default: 0 (unlimited)

These create implicit `*` scope limits if set. Explicit `usage_limits`
rows override them. Zero means disabled.

## What's out of scope

- Token-level metering (Claude API doesn't expose this via CLI)
- Billing, invoicing, payment processing
- External metering webhooks or Prometheus export
- Per-user limits (limits are per-group; users interact via groups)
- Credit purchase or top-up flows
- Real-time streaming cost estimates

## Implementation order

1. Migration 0027 + `store.RecordUsage` + call site in gateway
2. `DurationMs` field on `container.Output`
3. Migration 0028 + `store.SetLimit` / `MatchingLimit`
4. Pre-check in `processSenderBatch`
5. CLI commands (`limit set/rm/ls`, `usage`)
6. dashd `/usage` page
