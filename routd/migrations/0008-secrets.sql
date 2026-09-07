-- routd owns folder/user secrets (spec 5/5 § Daemon ownership). routd already
-- holds SECRETS_KEY for connector injection; it now OWNS the at-rest rows too,
-- so FolderSecretsResolved reads routd.db's own table instead of sibling-reading
-- gated's messages.db. Schema mirrors the FINAL messages.db shape (store 0034
-- created `secrets` with enc_value; 0047 renamed enc_value→value; 0048 added
-- secret_use_log) so store.FolderSecretsResolved + the audit-free writers read
-- it verbatim. secret_use_log has since become an objstore entity; see below.
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

-- secret_use_log is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An entity keeps the name
-- of the table it replaces, so a routd.db carrying rows here is moved by
-- `arizuko migrate-store` and refuses to open until it has been.
