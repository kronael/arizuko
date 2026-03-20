<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Task Scheduler

Cron-based task scheduling. Agents create tasks via IPC, gateway
polls for due tasks and runs them in containers.

## Three schedule types

- **cron** -- standard cron expression, timezone from `TIMEZONE` env
- **interval** -- integer milliseconds in the `cron` field; after each
  fire `next_run = now + ms` (not a cron expression)
- **once** -- ISO timestamp, runs once, no next_run after

## Two context modes

- **isolated** (default) -- fresh session per run, no history.
  Encoded as `sender = "scheduler-isolated"` in the message row;
  gateway detects this and does not reuse the group session.
- **group** -- reuses group's current session, sees prior conversation.
  Encoded as `sender = "scheduler"`.

`context_mode` column lives in `scheduled_tasks` (added in
`0011-task-run-logs.sql`). Read by `timed/main.go` in the fire query.

## Lifecycle state machine

```
created (active) -> due -> queued -> running -> completed
                                             -> error
active <-> paused (via pause_task / resume_task)
active -> deleted (via cancel_task)
```

Polled every `SCHEDULER_POLL_INTERVAL` (default 60s).
Due = `status = 'active' AND next_run <= now`.

## Queue integration

Tasks go through `GroupQueue.enqueueTask()`, sharing per-group
concurrency with user messages. A task won't run while a user
conversation is active on the same group.

## Run logging

Each task execution appended to `task_run_logs`:

```sql
CREATE TABLE task_run_logs (
  id          INTEGER PRIMARY KEY,
  task_id     INTEGER NOT NULL,
  run_at      TEXT NOT NULL,
  duration_ms INTEGER,
  status      TEXT NOT NULL,   -- success|error
  result      TEXT,
  error       TEXT
);
```

## Error handling

- Invalid group folder: task paused (stops retry churn)
- Group not found / container error: logged, run recorded as error
- All errors logged to `task_run_logs`
