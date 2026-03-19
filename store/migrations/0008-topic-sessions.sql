-- Recreate sessions table with topic column and composite PK.
-- Existing rows migrated with topic=''.

CREATE TABLE sessions_new (
  group_folder TEXT NOT NULL,
  topic        TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL,
  PRIMARY KEY (group_folder, topic)
);

INSERT INTO sessions_new (group_folder, topic, session_id)
  SELECT group_folder, '', session_id FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

ALTER TABLE messages ADD COLUMN topic TEXT NOT NULL DEFAULT '';

-- Ensure routes table exists (may have been added to 0001 after initial deployment).
CREATE TABLE IF NOT EXISTS routes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  jid TEXT NOT NULL,
  seq INTEGER NOT NULL DEFAULT 0,
  type TEXT NOT NULL DEFAULT 'default',
  match TEXT,
  target TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_routes_jid_seq ON routes(jid, seq);

-- INSERT OR IGNORE semantics for prefix routes.
-- Unique constraint on (jid, seq, match) so duplicates are silently skipped.
CREATE UNIQUE INDEX IF NOT EXISTS idx_routes_jid_seq_match ON routes(jid, seq, COALESCE(match, ''));
