# 055 — seed bookkeeping cron tasks for all groups

Legacy-only: new groups are seeded at creation by `store.SeedDefaultTasks`.
This migration fills the gap for groups created before that seeding existed.

Groups created outside `onbod /approve` (CLI, IPC, manual SQL) lack the
5 compact-memories scheduled tasks. Seed them for each group returned
by `refresh_groups`:

1. `list_tasks` — count entries whose prompt contains `compact-memories`; if ≥5, skip.
2. Pick the targetJid from `list_routes`:
   - `platform=X`+`room=Y` → `X:Y`; `room=Y` alone → `Y`; `chat_jid=Z` → `Z`
   - Skip wildcards (`*`, `?`)
   - Fallback: `local:<folder>`
3. For each missing task below, call `schedule_task`:

| prompt                             | cron         | contextMode |
| ---------------------------------- | ------------ | ----------- |
| `/compact-memories episodes day`   | `0 2 * * *`  | `isolated`  |
| `/compact-memories episodes week`  | `0 3 * * 1`  | `isolated`  |
| `/compact-memories episodes month` | `0 4 1 * *`  | `isolated`  |
| `/compact-memories diary week`     | `0 3 * * 1`  | `isolated`  |
| `/compact-memories diary month`    | `0 4 1 * *`  | `isolated`  |

Use MCP tools — no SQL or shell. Check each task individually (partial
sets are possible).
