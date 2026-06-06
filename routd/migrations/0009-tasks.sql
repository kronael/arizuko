-- routd owns scheduled tasks (spec 5/5 § Daemon ownership). routd is the central
-- data plane (acl 0007, secrets 0008); it now OWNS the at-rest task rows too, so
-- the tasks.json spawn snapshot + the schedule/pause/resume/cancel agent tools
-- read+write routd.db's own tables instead of sibling-reading gated's
-- messages.db. Schema mirrors the FINAL messages.db shape (store 0001 created
-- `scheduled_tasks`; store 0011 added the `context_mode` column + created
-- `task_run_logs`) so store.ListTasks/GetTask/TaskRunLogs + the audit-free
-- writers read it verbatim. timed reads due tasks + records run logs via routd
-- HTTP endpoints (GET /v1/tasks/due, POST /v1/tasks/runlog).
CREATE TABLE scheduled_tasks (
  id           TEXT PRIMARY KEY,
  owner        TEXT NOT NULL,
  chat_jid     TEXT NOT NULL,
  prompt       TEXT NOT NULL,
  cron         TEXT,
  next_run     TEXT,
  status       TEXT NOT NULL DEFAULT 'active',
  created_at   TEXT NOT NULL,
  context_mode TEXT NOT NULL DEFAULT 'group'
);
CREATE INDEX idx_tasks_next ON scheduled_tasks(status, next_run);

CREATE TABLE task_run_logs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id     TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE CASCADE,
  run_at      TEXT NOT NULL,
  duration_ms INTEGER,
  status      TEXT NOT NULL,
  result      TEXT,
  error       TEXT
);
CREATE INDEX idx_task_run_logs_task ON task_run_logs(task_id);
