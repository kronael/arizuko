-- routd owns routd.db and emits audit events into it. Before this migration,
-- routd.db had NO audit_log table, so routd's own mutation paths (acl/secrets/
-- tasks) called audit-free store variants (PutACLRow, RemoveACLRowBare, taskStore)
-- — their audit.EmitInTx would have failed "no such table" and rolled back the
-- mutation. dashd/proxyd/webd routed their audit.Init at the frozen pre-split
-- messages.db instead, leaving it a live 8th DB purely for this sink. This table
-- (mirroring store/migrations/0066 + authd 0003 + onbod 0002 exactly —
-- audit.insertSQL writes every DB through one shape) lets routd's ACL/secret/task
-- mutations use the AUDITED store variants (AddACLRow, SetSecret, CreateTask), so
-- audit lands in the owner DB and messages.db can retire.
--
-- Existing rows are copied from the sibling messages.db by routd.Open
-- (copyLegacyAuditLog) in autocommit AFTER migrations — ATTACH can't run inside
-- the migration runner's BEGIN EXCLUSIVE. The messages.db copy stays intact as
-- a one-release fallback (no DROP here). Spec: specs/5/I-tool-call-logging.md.
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
