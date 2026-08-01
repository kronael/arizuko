---
status: shipped
supersedes: [4/task-logs.md]
---

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

Schema lives in `store/migrations/` and is owned by `routd`
(`scheduled_tasks` + `task_run_logs` moved to `routd.db` at the split);
`timed` is a read/write client that never runs migrations.

- `owner` — group folder that created the task. Used by
  ipc/auth for authorization.
- `cron` — cron expression. NULL for one-shot tasks.
- `next_run` — when to fire next. One-shot: set directly,
  goes NULL after firing. Cron: recomputed after each fire.
- `status` — `active`, `paused`, `firing` (transient claim during
  fire loop), or `completed` (one-shot terminal).

## Implemented beyond base spec

- `context_mode` column: `"group"` (resumes session) | `"isolated"` (no
  `--resume`). Encoded as sender `"timed-isolated:<task_id>"` in messages
  when isolated; default sender `"timed"` resumes group session.
- Interval mode: if `cron` field is a plain integer, treated as milliseconds
  interval; `next_run = now + ms` after each fire.

No `schedule_type`, no `last_run`, no `last_result`. Cron
covers intervals. One-shot is just NULL cron + set next_run.

## `task_run_logs` — execution history

```sql
CREATE TABLE task_run_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
  run_at      TEXT NOT NULL,   -- RFC3339, when the task fired
  duration_ms INTEGER,         -- fire → log write
  status      TEXT NOT NULL,   -- 'success' | 'error'
  result      TEXT,            -- reserved; timed never writes it
  error       TEXT             -- err.Error() on 'error' rows, NULL on success
);
```

`logRun(db, taskID, status, errText, durationMs)` writes one row per
fire. `success` means the message row was INSERTed — **not** that the
agent ran. The `messages` table stays the primary audit trail of what
the agent processed; `task_run_logs` only adds enqueue outcome and
timing, which `messages` cannot express (a failed INSERT leaves no
message row at all).

`result` is reserved for a future payload (container exit status, agent
output snippet) and is never populated today.

## Loop

```
every 60s:
  SELECT id, chat_jid, prompt, cron FROM scheduled_tasks
    WHERE status = 'active' AND next_run <= now

  for each task:
    INSERT INTO messages (sender='timed' or 'timed-isolated:<id>')
    if cron IS NOT NULL: next_run = next_cron(cron)
    else: next_run = NULL
```

## Task management is not timed's job

`timed` only runs the poll loop. Creating, listing, pausing and
cancelling tasks are CRUD on `scheduled_tasks`, and that rides the
`scheduled_tasks` resreg resource like every other cold-tier
management entity — one handler, an MCP face for agents and a REST
face for operators ([`../5/16-mcp-rest-unification.md`](../5/16-mcp-rest-unification.md),
mechanism in [`../5/17-openapi-mcp.md`](../5/17-openapi-mcp.md)).

The only rule that is the scheduler's own: `next_run` is **computed**
from the `cron` expression, never accepted as a caller parameter. A
one-shot task is the degenerate case — no `cron`, `next_run` set
directly, NULL afterwards.

## Implementation

`timed/main.go` is the poll loop; `timed/split.go` carries the
non-mounted write path. `timed` has no filesystem mount on the owner's
DB, so it writes through routd's HTTP API with a service token rather
than opening `routd.db` directly — the split write-discipline every
non-mounted daemon follows.

`scheduled_tasks` arrived in migration 0001; `task_run_logs` and
`context_mode` in `store/migrations/0011-task-run-logs.sql`.
