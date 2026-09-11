-- routd.db initial schema (spec 5/E § routd.db schema). routd owns this
-- DB and its own migration sequence (service="routd"). Times are
-- RFC3339Nano UTC TEXT throughout — the Go layer computes every timestamp.
--
-- `groups`, `routes`, `web_routes`, `route_tokens`, `chats`, `sessions`,
-- `chat_reply_state`, `system_messages`, `turn_context`, `turn_results` and
-- `group_watchers` are objstore entities rather than tables this chain creates:
-- the driver makes each one, from the declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An entity keeps the name
-- of the table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-routd` or `arizuko migrate-hot` and refuses to open until it
-- has been.

CREATE TABLE messages (
  id              TEXT PRIMARY KEY,
  chat_jid        TEXT NOT NULL,
  sender          TEXT NOT NULL,
  sender_name     TEXT,
  content         TEXT NOT NULL,
  timestamp       TEXT NOT NULL,
  is_from_me      INTEGER NOT NULL DEFAULT 0,
  is_bot_message  INTEGER NOT NULL DEFAULT 0,
  forwarded_from  TEXT,
  reply_to_id     TEXT,
  reply_to_text   TEXT,
  reply_to_sender TEXT,
  topic           TEXT NOT NULL DEFAULT '',
  routed_to       TEXT NOT NULL DEFAULT '',
  verb            TEXT NOT NULL DEFAULT 'message',
  attachments     TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  is_observed     INTEGER NOT NULL DEFAULT 0,
  turn_id         TEXT,
  status          TEXT NOT NULL DEFAULT 'sent',
  platform_id     TEXT,
  chat_name       TEXT NOT NULL DEFAULT ''
);
CREATE INDEX idx_messages_chat_ts ON messages(chat_jid, timestamp);
CREATE INDEX idx_messages_observed ON messages(routed_to, is_observed, timestamp);
CREATE INDEX idx_messages_turn_id ON messages(turn_id) WHERE turn_id IS NOT NULL;
CREATE INDEX idx_messages_status ON messages(status) WHERE status != 'sent';

CREATE VIRTUAL TABLE messages_fts USING fts5(
  content, content='messages', content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);
CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.rowid, old.content);
END;
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content) VALUES('delete', old.rowid, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
