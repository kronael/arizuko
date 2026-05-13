-- Spec 9/11 broker audit table. One row per (tool call × resolved key).
-- No secret values; only key, scope, status, latency.
CREATE TABLE IF NOT EXISTS secret_use_log (
  ts          TEXT NOT NULL,
  spawn_id    TEXT NOT NULL DEFAULT '',
  caller_sub  TEXT NOT NULL DEFAULT '',
  folder      TEXT NOT NULL DEFAULT '',
  tool        TEXT NOT NULL,
  key         TEXT NOT NULL,
  scope       TEXT NOT NULL,
  status      TEXT NOT NULL,
  latency_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_secret_use_log_ts ON secret_use_log(ts);
