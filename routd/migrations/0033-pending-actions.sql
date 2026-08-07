-- pending_actions: a tool call suspended until a human approves it (spec 5/19).
--
-- The hold RULE is an ordinary acl row (`hold:mcp:<tool>`); this table holds the
-- suspended CALL, which is per-invocation state and belongs nowhere else.
--
-- args_hash is the canonical-JSON hash of args_final. Release matches on
-- (folder, tool, args_hash), so an agent that re-issues the call with different
-- arguments misses and is held again — edited-args enforcement by construction
-- rather than by a second comparison the reviewer has to trust.
--
-- Expiry is lazy: a reader compares expires_at to now. No GC job, because a row
-- nobody reads costs nothing and a job that deletes evidence is worse.
CREATE TABLE IF NOT EXISTS pending_actions (
  id            TEXT PRIMARY KEY,
  group_folder  TEXT NOT NULL,
  caller_agent  TEXT NOT NULL,
  tool          TEXT NOT NULL,
  args          TEXT NOT NULL DEFAULT '{}',
  args_final    TEXT NOT NULL DEFAULT '{}',
  args_hash     TEXT NOT NULL DEFAULT '',
  status        TEXT NOT NULL DEFAULT 'held',
  chat_jid      TEXT NOT NULL DEFAULT '',
  created_at    TEXT NOT NULL,
  reviewed_by   TEXT NOT NULL DEFAULT '',
  reviewed_at   TEXT NOT NULL DEFAULT '',
  reviewer_note TEXT NOT NULL DEFAULT '',
  result        TEXT NOT NULL DEFAULT '',
  error         TEXT NOT NULL DEFAULT '',
  expires_at    TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS pending_actions_by_folder  ON pending_actions(group_folder, status);
CREATE INDEX IF NOT EXISTS pending_actions_by_release ON pending_actions(group_folder, tool, args_hash, status);
