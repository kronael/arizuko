---
status: draft
---

## status: shipped

# task_run_logs

Execution history for scheduled tasks. Added in timed migration 0002.

## Schema

```sql
CREATE TABLE IF NOT EXISTS task_run_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
  run_at      TEXT NOT NULL,
  duration_ms INTEGER,
  status      TEXT NOT NULL,
  result      TEXT,
  error       TEXT
);
CREATE INDEX IF NOT EXISTS idx_task_run_logs_task ON task_run_logs(task_id);
```

## Fields

| Column        | Type    | Description                                    |
| ------------- | ------- | ---------------------------------------------- |
| `id`          | INTEGER | autoincrement row id                           |
| `task_id`     | TEXT    | FK → `scheduled_tasks.id`, cascades on delete  |
| `run_at`      | TEXT    | RFC3339 timestamp when task fired              |
| `duration_ms` | INTEGER | elapsed milliseconds from fire to log write    |
| `status`      | TEXT    | `"success"` or `"error"`                       |
| `result`      | TEXT    | reserved; not populated by timed (always NULL) |
| `error`       | TEXT    | reserved; not populated by timed (always NULL) |

## How timed populates it

`logRun(db, taskID, status, durationMs)` is called after each task fires:

- `"success"` — message inserted into `messages` table, `next_run` updated
- `"error"` — INSERT into `messages` failed (DB error)

`result` and `error` columns exist in the schema for future use (e.g.
container exit status, agent output snippet) but are not written today.

## Relationship to messages table

`messages` is the primary audit trail for what the agent processed.
`task_run_logs` supplements it with: whether the message was successfully
enqueued, and timing. A `success` log does not mean the agent ran — it
means the message was inserted.

## Migration context

Migration 0002 also adds `context_mode TEXT NOT NULL DEFAULT 'group'` to
`scheduled_tasks`. This enables `"isolated"` mode where the task fires
with sender `"scheduler-isolated"` (no `--resume`, fresh context).
