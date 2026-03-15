<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Task Scheduler

Cron-based task scheduling. Agents create tasks via IPC, gateway
polls for due tasks and runs them in containers.

## Three schedule types

- **cron** -- standard cron expression, timezone from `TIMEZONE` env
- **interval** -- milliseconds between runs (next = now + ms)
- **once** -- ISO timestamp, runs once, no next_run after

## Two context modes

- **isolated** (default) -- fresh session per run, no history
- **group** -- reuses group's current session, sees prior conversation

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

## Error handling

- Invalid group folder: task paused (stops retry churn)
- Group not found / container error: logged, run recorded as error
- All errors logged to `task_run_logs`
