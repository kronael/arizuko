-- Phase C of specs/7/35: encrypted secrets table.
-- scope_kind = "folder" (path glob) or "user" (auth_users.sub).
-- enc_value is AES-GCM(AUTH_SECRET) of the plaintext value.
CREATE TABLE secrets (
  scope_kind  TEXT NOT NULL,
  scope_id    TEXT NOT NULL,
  key         TEXT NOT NULL,
  enc_value   BLOB NOT NULL,
  created_at  TEXT NOT NULL,
  PRIMARY KEY (scope_kind, scope_id, key)
);
CREATE INDEX idx_secrets_folder ON secrets(scope_id) WHERE scope_kind = 'folder';
CREATE INDEX idx_secrets_user   ON secrets(scope_id) WHERE scope_kind = 'user';
