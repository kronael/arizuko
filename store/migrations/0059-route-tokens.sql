CREATE TABLE route_tokens (
  token_hash    BLOB PRIMARY KEY,
  jid           TEXT NOT NULL,
  owner_folder  TEXT NOT NULL,
  created_at    TEXT NOT NULL
);
CREATE INDEX route_tokens_jid ON route_tokens(jid);
ALTER TABLE groups DROP COLUMN slink_token;
