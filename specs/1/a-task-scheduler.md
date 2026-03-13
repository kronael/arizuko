---
status: shipped
---

# Task Scheduler

## Overview

Cron-based task scheduling. Agents create tasks via IPC actions,
gateway polls for due tasks and runs them in containers.

## Schedule types

Three schedule types:

- **cron** — standard cron expression, parsed with cron-parser.
  Timezone from `TIMEZONE` env var.
- **interval** — milliseconds between runs. Next run = now + ms.
- **once** — ISO timestamp. Runs once, no next_run after.

## Context modes

- **isolated** (default) — fresh session per run. No conversation
  history, no prior context. Clean slate each time.
- **group** — reuses the group's current session. Task sees prior
  conversation history. Useful for tasks that build on earlier state.

## Task lifecycle

```
created (active) → due → queued → running → completed
                                          → error
active ↔ paused (via pause_task / resume_task)
active → row deleted (via cancel_task)
```

Tasks are polled every `SCHEDULER_POLL_INTERVAL` (default 60s).
Due tasks: `status = 'active' AND next_run <= now`.

After each run:

- Log run to `task_run_logs` table (duration, status, result, error)
- Compute next_run (cron/interval) or null (once)
- Store result summary (first 200 chars)

## Container execution

Tasks run via `runContainerCommand`, same as user messages.
Differences:

- `isScheduledTask: true` in container input
- No channel name (no output style applied)
- 10s close delay after result (vs IDLE_TIMEOUT for user sessions)
- Single-turn: container closes after producing result

Task output is sent to `chat_jid` via `sendMessage`. The task's
JID determines which channel receives the output.

## Queue integration

Tasks go through `GroupQueue.enqueueTask()`, sharing the
per-group concurrency model with user messages. A task won't
run while a user conversation is active on the same group.

## IPC actions

### schedule_task

Create a new scheduled task.

```typescript
{
  targetJid: string,        // JID to receive output
  prompt: string,           // what the agent should do
  schedule_type: 'cron' | 'interval' | 'once',
  schedule_value: string,   // cron expr, ms, or ISO timestamp
  context_mode?: 'group' | 'isolated',  // default: isolated
}
```

Authorization: root can schedule on any group. Non-root can
only schedule on own group (`targetFolder === sourceGroup`).

### pause_task / resume_task / cancel_task

Operate on existing tasks by ID. Same authorization: root
can touch any task, non-root only own group's tasks.

## DB schema

```sql
CREATE TABLE scheduled_tasks (
  id TEXT PRIMARY KEY,
  group_folder TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL,
  schedule_type TEXT NOT NULL,  -- cron | interval | once
  schedule_value TEXT NOT NULL,
  next_run TEXT,
  last_run TEXT,
  status TEXT DEFAULT 'active', -- active | paused | completed
  last_result TEXT,
  created_at TEXT NOT NULL
);
-- context_mode TEXT DEFAULT 'isolated' added via migration

CREATE TABLE task_run_logs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id TEXT NOT NULL,
  run_at TEXT NOT NULL,
  duration_ms INTEGER NOT NULL,
  status TEXT NOT NULL,  -- success | error
  result TEXT,
  error TEXT
);
```

## Tasks snapshot

Before each task run, gateway writes a JSON snapshot of all
tasks (across all groups) to the group's data dir. The agent
can read this to see what tasks exist.

## Key files

- `src/task-scheduler.ts` — poll loop, task execution
- `src/actions/tasks.ts` — IPC actions (schedule, pause, resume, cancel)
- `src/db.ts` — task CRUD, getDueTasks, logTaskRun

## Error handling

- Invalid group folder → task paused (stops retry churn)
- Group not found → logged, run recorded as error
- Container error → logged, run recorded as error
- All errors logged to task_run_logs for debugging
