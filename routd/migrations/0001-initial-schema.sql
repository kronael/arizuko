-- routd.db initial schema (spec 5/E § routd.db schema). routd owns this
-- DB and its own migration sequence (service="routd"). Times are
-- RFC3339Nano UTC TEXT throughout — the Go layer computes every timestamp.

CREATE TABLE groups (
  folder                  TEXT PRIMARY KEY,
  added_at                TEXT NOT NULL,
  container_config        TEXT,
  updated_at              TEXT,
  product                 TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day  INTEGER NOT NULL DEFAULT 0,
  open                    INTEGER NOT NULL DEFAULT 1,
  observe_window_messages INTEGER,
  observe_window_chars    INTEGER,
  model                   TEXT
);

CREATE TABLE chats (
  jid          TEXT PRIMARY KEY,
  errored      INTEGER NOT NULL DEFAULT 0,
  agent_cursor TEXT,
  sticky_group TEXT,
  sticky_topic TEXT,
  is_group     INTEGER NOT NULL DEFAULT 0
);

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

CREATE TABLE routes (
  id                       INTEGER PRIMARY KEY AUTOINCREMENT,
  seq                      INTEGER NOT NULL DEFAULT 0,
  match                    TEXT    NOT NULL DEFAULT '',
  target                   TEXT    NOT NULL,
  observe_window_messages  INTEGER,
  observe_window_chars     INTEGER
);
CREATE INDEX idx_routes_seq ON routes(seq);

CREATE TABLE sessions (
  group_folder    TEXT NOT NULL,
  topic           TEXT NOT NULL DEFAULT '',
  session_id      TEXT NOT NULL,
  parent_topic    TEXT,
  forked_at       TEXT,
  observed_cursor TEXT,
  PRIMARY KEY (group_folder, topic)
);
CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);

CREATE TABLE chat_reply_state (
  jid            TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  last_reply_id  TEXT NOT NULL,
  engaged_until  TEXT,
  engaged_folder TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (jid, topic)
);

CREATE TABLE turn_context (
  turn_id        TEXT PRIMARY KEY,
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  chat_jid       TEXT NOT NULL,
  trigger_sender TEXT NOT NULL,
  started_at     TEXT NOT NULL,
  run_id         TEXT,
  state          TEXT NOT NULL DEFAULT 'running'
);

CREATE TABLE turn_results (
  folder       TEXT NOT NULL,
  turn_id      TEXT NOT NULL,
  session_id   TEXT,
  status       TEXT NOT NULL,
  recorded_at  TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id)
);

CREATE TABLE cost_log (
  folder       TEXT NOT NULL,
  turn_id      TEXT NOT NULL,
  model        TEXT NOT NULL,
  input_tokens  INTEGER NOT NULL DEFAULT 0,
  output_tokens INTEGER NOT NULL DEFAULT 0,
  cost_cents   INTEGER NOT NULL DEFAULT 0,
  recorded_at  TEXT NOT NULL,
  PRIMARY KEY (folder, turn_id, model)
);
CREATE INDEX idx_cost_log_folder_day ON cost_log(folder, recorded_at);

CREATE TABLE web_routes (
  path_prefix TEXT PRIMARY KEY,
  access      TEXT NOT NULL CHECK(access IN ('public','auth','deny','redirect')),
  redirect_to TEXT,
  folder      TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
  created_at  TEXT NOT NULL
);

CREATE TABLE route_tokens (
  token_hash    BLOB PRIMARY KEY,
  jid           TEXT NOT NULL,
  owner_folder  TEXT NOT NULL REFERENCES groups(folder) ON DELETE CASCADE,
  created_at    TEXT NOT NULL
);
CREATE INDEX route_tokens_jid ON route_tokens(jid);

CREATE TABLE system_messages (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  folder  TEXT NOT NULL,
  source  TEXT NOT NULL,
  kind    TEXT NOT NULL,
  body    TEXT NOT NULL,
  created TEXT NOT NULL
);

CREATE TABLE group_watchers (
  observer TEXT NOT NULL,
  source   TEXT NOT NULL,
  PRIMARY KEY (observer, source)
);

CREATE TABLE idempotency_keys (
  endpoint     TEXT NOT NULL,
  key          TEXT NOT NULL,
  request_hash TEXT NOT NULL,
  status       INTEGER NOT NULL,
  response     TEXT NOT NULL,
  created_at   TEXT NOT NULL,
  expires_at   TEXT NOT NULL,
  PRIMARY KEY (endpoint, key)
);
CREATE INDEX idx_idempotency_expiry ON idempotency_keys(expires_at);
