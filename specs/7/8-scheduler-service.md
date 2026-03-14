# Scheduler Microservice

## Role

A cron daemon that writes messages. Polls `scheduled_tasks`
for due items, inserts into `messages`. That's it.

The scheduler does NOT:

- Run containers or agents
- Know about MCP, docker, sessions, or volumes
- Deliver messages to channels
- Track results or execution status

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

- `owner` — group folder that created the task. Used by MCP
  actions for authorization (can this agent touch this task?).
- `cron` — cron expression. NULL for one-shot tasks.
- `next_run` — when to fire next. For one-shot: set directly,
  goes NULL after firing. For cron: recomputed after each fire.
- `status` — `active` or `paused`. No other states.

One-shot tasks: set `next_run` to an ISO timestamp, leave
`cron` null. After firing, `next_run` goes null. The task
never fires again. No special type, no special status.

## Loop

```
every poll_interval:
  SELECT * FROM scheduled_tasks
    WHERE status = 'active' AND next_run <= now

  for each task:
    INSERT INTO messages (id, chat_jid, sender, content, timestamp)
    if task.cron IS NOT NULL:
      UPDATE next_run = next_cron(task.cron)
    else:
      UPDATE next_run = NULL
```

## Deps

```go
type Scheduler struct {
  db       *sql.DB
  timezone string
}
```

No gateway, no queue, no channels, no groups map.

## MCP Actions

Not part of the scheduler process. The IPC server exposes
these as MCP tools to agents — they're just DB writes.

### schedule_task

```
input: chat_jid, prompt, cron (optional), next_run (optional)
auth:  tier 0 anywhere, tier 1 own world, tier 2 own group
→ INSERT INTO scheduled_tasks
```

### pause_task / resume_task

```
input: task_id
auth:  task.owner must match caller's group (or root)
→ UPDATE status = 'paused' | 'active'
```

### cancel_task

```
input: task_id
auth:  same as pause
→ DELETE FROM scheduled_tasks WHERE id = ?
```

## Migration

Own runner, own service name:

```sql
-- scheduler:0001
CREATE TABLE IF NOT EXISTS scheduled_tasks (
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

## Messages as Log

The scheduler's audit trail is the `messages` table.
Every fire is a row with `sender='scheduler'`. No separate
`task_run_logs` table. If you want to know what the scheduler
did, query messages.
