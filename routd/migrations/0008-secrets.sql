-- routd owns folder/user secrets (spec 5/5 § Daemon ownership). routd already
-- holds SECRETS_KEY for connector injection; it now OWNS the at-rest rows too,
-- so FolderSecretsResolved reads routd.db's own table instead of sibling-reading
-- gated's messages.db. Schema mirrors the FINAL messages.db shape (store 0034
-- created `secrets` with enc_value; 0047 renamed enc_value→value; 0048 added
-- secret_use_log) so store.FolderSecretsResolved + the audit-free writers read
-- it verbatim.
CREATE TABLE secrets (
  scope_kind  TEXT NOT NULL,
  scope_id    TEXT NOT NULL,
  key         TEXT NOT NULL,
  value       BLOB NOT NULL,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (scope_kind, scope_id, key)
);
CREATE INDEX idx_secrets_folder ON secrets(scope_id) WHERE scope_kind = 'folder';
CREATE INDEX idx_secrets_user   ON secrets(scope_id) WHERE scope_kind = 'user';

CREATE TABLE secret_use_log (
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
CREATE INDEX idx_secret_use_log_ts ON secret_use_log(ts);
