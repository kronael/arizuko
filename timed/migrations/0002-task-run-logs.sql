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
ALTER TABLE scheduled_tasks ADD COLUMN context_mode TEXT NOT NULL DEFAULT 'group';
