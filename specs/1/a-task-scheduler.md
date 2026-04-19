---
status: shipped
---

# Task Scheduler

Cron-based task scheduling. Agents create tasks via IPC, `timed`
daemon polls for due tasks and writes them to messages.

Daemon: `timed/main.go`. Full service spec: `specs/4/8-scheduler-service.md`.

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

Tasks flow through the normal message pipeline. `timed` writes a
message row; `gated` picks it up via `GroupQueue`, sharing per-group
concurrency with user messages.

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
