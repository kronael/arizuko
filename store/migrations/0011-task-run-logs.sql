-- task_run_logs: one row per execution of a scheduled task.
--
-- task_id carries no foreign key, and `context_mode` — whether a run carries
-- the group's transcript or none — is an attribute of the task itself.
-- `scheduled_tasks` is an objstore entity rather than a table this chain
-- creates: the driver makes it, from the one declaration that sits beside the
-- code writing it (store.EnsureEntities, spec 6/37 step 7), addressed by its
-- id. An entity has no column a REFERENCES clause can name and needs no ALTER
-- to start carrying an attribute, so a run log outlives the task that produced
-- it: store.DeleteTask removes the task and nothing else.
CREATE TABLE IF NOT EXISTS task_run_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL,
  run_at      TEXT NOT NULL,
  duration_ms INTEGER,
  status      TEXT NOT NULL,
  result      TEXT,
  error       TEXT
);
CREATE INDEX IF NOT EXISTS idx_task_run_logs_task ON task_run_logs(task_id);
