-- chat_sessions — the dashd chat-portal's continue-link index.
--
-- A "session" is a web: route token. route_tokens stores only the token HASH,
-- so the portal cannot rebuild a continue link from it; this table records the
-- RAW token at mint time (the same one-shot exposure as the widget's own
-- bootstrap, persisted for the operator who minted it) plus an optional label.
--
-- It was never a migrated table: dashd created it lazily with CREATE TABLE IF
-- NOT EXISTS inside the pre-split messages.db, which is why it exists on one
-- instance and not the others (empty on all three). routd owns routd.db's
-- schema, so the table is declared here; dashd remains the only writer, going
-- through its FS-mounted routd.db handle (split write-discipline), exactly as
-- it does for acl/secrets/route_tokens.
--
-- No backfill: the pre-split copies hold zero rows fleet-wide.
CREATE TABLE IF NOT EXISTS chat_sessions (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  folder     TEXT NOT NULL,
  token      TEXT NOT NULL UNIQUE,
  label      TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS chat_sessions_folder ON chat_sessions(folder, created_at);
