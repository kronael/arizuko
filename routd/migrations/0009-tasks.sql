-- task_run_logs: one row per execution of a scheduled task. timed writes them
-- through routd's HTTP endpoints (GET /v1/tasks/due, POST /v1/tasks/runlog)
-- because timed has no mount of routd.db; store.TaskRunLogs and
-- store.AllRunLogs read them back for the operator.
--
-- task_id carries no foreign key. `scheduled_tasks` is an objstore entity
-- rather than a table this chain creates — the driver makes it, from the one
-- declaration that sits beside the code writing it (store.EnsureEntities, spec
-- 6/37 step 7), addressed by its id — and an entity has no column a REFERENCES
-- clause can name. So a run log outlives the task that produced it:
-- store.DeleteTask removes the task and nothing else. An entity keeps the name
-- of the table it replaces, so a database carrying task rows is moved by
-- `arizuko migrate-routd` and refuses to open until it has been.
CREATE TABLE task_run_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL,
  run_at      TEXT NOT NULL,
  duration_ms INTEGER,
  status      TEXT NOT NULL,
  result      TEXT,
  error       TEXT
);
CREATE INDEX idx_task_run_logs_task ON task_run_logs(task_id);
