# 055 — seed bookkeeping cron tasks for all groups

## Goal

Groups created outside onbod `/approve` (CLI, IPC, manual SQL) lack the
5 compact-memories scheduled tasks. This migration seeds them.

## Check

Per group: if `list_tasks` already shows 5+ tasks matching
`compact-memories`, skip that group.

## Steps

For each group returned by `refresh_groups`:

1. Call `list_tasks` — count entries whose prompt contains `compact-memories`
2. If count >= 5, skip (already seeded)
3. Call `list_routes` — find a route whose target matches this group's
   folder. Extract the JID from the route's match field (strip `room=`
   prefix). If no route exists, use `local:<folder>` as the targetJid.
4. For each missing task from the 5 below, call `schedule_task`:

| prompt | cron | contextMode |
|--------|------|-------------|
| `/compact-memories episodes day` | `0 2 * * *` | `isolated` |
| `/compact-memories episodes week` | `0 3 * * 1` | `isolated` |
| `/compact-memories episodes month` | `0 4 1 * *` | `isolated` |
| `/compact-memories diary week` | `0 3 * * 1` | `isolated` |
| `/compact-memories diary month` | `0 4 1 * *` | `isolated` |

Use the MCP tools directly — do not write SQL or shell commands.
Check each task individually; some groups may have partial sets.

## After

```bash
echo 55 > ~/.claude/skills/self/MIGRATION_VERSION
```
