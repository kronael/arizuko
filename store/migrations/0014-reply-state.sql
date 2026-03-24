CREATE TABLE IF NOT EXISTS chat_reply_state (
  jid   TEXT NOT NULL,
  topic TEXT NOT NULL DEFAULT '',
  last_reply_id TEXT NOT NULL,
  PRIMARY KEY (jid, topic)
);
