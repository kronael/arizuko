# timed

Cron daemon. Polls `scheduled_tasks`, INSERTs into `messages`.

## Table

```sql
CREATE TABLE scheduled_tasks (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL,
  cron TEXT,
  next_run TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL
);
```

Migration service name: `timed`.

- `owner` — group folder that created the task. Used by
  ipc/auth for authorization.
- `cron` — cron expression. NULL for one-shot tasks.
- `next_run` — when to fire next. One-shot: set directly,
  goes NULL after firing. Cron: recomputed after each fire.
- `status` — `active` or `paused`. No other states.

No `schedule_type`, no `interval`, no `context_mode`, no
`last_run`, no `last_result`, no `task_run_logs`. Cron
covers intervals. One-shot is just NULL cron + set next_run.
Messages table is the audit trail.

## Loop

```
every 60s:
  SELECT id, chat_jid, prompt, cron FROM scheduled_tasks
    WHERE status = 'active' AND next_run <= now

  for each task:
    INSERT INTO messages (sender='scheduler')
    if cron IS NOT NULL: next_run = next_cron(cron)
    else: next_run = NULL
```

## MCP Actions

Handled by ipc directly. The agent calls the MCP tool,
ipc stamps identity, calls auth.Authorize, then
executes the tool inline (reads/writes scheduled_tasks
table). timed only does the cron poll loop.

### schedule_task

```
input: targetJid, prompt, cron (optional)
auth:  task.owner checked by auth (tier-based)
→ INSERT INTO scheduled_tasks
```

`next_run` is computed from `cron` expression, not a user param.
One-shot tasks: omit `cron`, set `next_run` directly in DB.

### list_tasks

```
input: (none, or optional owner filter)
auth:  caller identity checked by auth (tier-based)
→ SELECT FROM scheduled_tasks WHERE owner matches caller
```

### pause_task / resume_task

```
input: taskId
auth:  task.owner must match caller (checked by auth)
→ UPDATE status = 'paused' | 'active'
```

### cancel_task

```
input: taskId
→ DELETE FROM scheduled_tasks WHERE id = ?
```

## Layout

```
timed/
  main.go
  migrations/
    0001-schema.sql
```

## Implementation

`timed/main.go` — ~150 LOC. Zero dependencies on
gateway, store, core, or any arizuko package. Just
`database/sql`, `robfig/cron`, `modernc.org/sqlite`.
