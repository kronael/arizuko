-- runed owns runed.db and emits audit events into it (main.go audit.Init).
-- Until this migration runed declared no audit_log at all, so every emit would
-- have failed "no such table" — which is why runed shipped with no audit call
-- sites: there was nowhere to write. Each daemon owns + migrates its own DB and
-- its own audit table (specs/5/I § "audit_log is per-daemon", 5/E, 5/P);
-- correlation across them is turn_id, not a shared table.
--
-- Columns mirror routd/migrations/0016 + authd 0003 + onbod 0002 + store 0066
-- exactly — audit.insertSQL writes every DB through one shape.
-- Spec: specs/5/I-tool-call-logging.md.
CREATE TABLE IF NOT EXISTS audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  category        TEXT    NOT NULL,
  action          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  actor_sub       TEXT,
  resource        TEXT,
  scope           TEXT,
  surface         TEXT,
  params_summary  TEXT,
  outcome         TEXT    NOT NULL,
  error_msg       TEXT,
  duration_ms     INTEGER,
  turn_id         TEXT,
  folder          TEXT,
  instance        TEXT,
  request_id      TEXT,
  source_ip       TEXT
);

CREATE INDEX IF NOT EXISTS audit_log_created_at ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS audit_log_actor_sub  ON audit_log(actor_sub) WHERE actor_sub IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_log_folder     ON audit_log(folder)    WHERE folder    IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_log_cat_act    ON audit_log(category, action);
