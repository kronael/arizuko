-- authd owns auth.db and emits audit events into it (main.go audit.Init).
-- The shared audit_log table is created in messages.db by store/migrations/0066,
-- but authd never migrates messages.db (CLAUDE.md DB-ownership rule), so its
-- own DB needs the same table or every emit warns "no such table" and drops.
-- Columns mirror store/migrations/0066-audit-log.sql exactly (audit.insertSQL
-- writes both DBs through one shape). Spec: specs/5/I-tool-call-logging.md.
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
