-- 0070 — messages_fts virtual table + sync triggers
--
-- FTS5 shadow over messages.content, kept in sync by triggers. Backs
-- the `find_messages` MCP tool (spec 5/C). messages.id is TEXT, so we
-- key the shadow on the implicit INTEGER `rowid` SQLite gives every
-- non-WITHOUT-ROWID table.
--
-- Tokenizer: unicode61 + remove_diacritics=2 — handles Czech, Spanish,
-- Japanese etc. without surprises (Czech "úroveň" matches "uroven").

CREATE VIRTUAL TABLE messages_fts USING fts5(
  content,
  content='messages',
  content_rowid='rowid',
  tokenize='unicode61 remove_diacritics 2'
);

INSERT INTO messages_fts(rowid, content)
  SELECT rowid, content FROM messages;

CREATE TRIGGER messages_fts_ai AFTER INSERT ON messages BEGIN
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER messages_fts_au AFTER UPDATE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.rowid, old.content);
  INSERT INTO messages_fts(rowid, content) VALUES (new.rowid, new.content);
END;
CREATE TRIGGER messages_fts_ad AFTER DELETE ON messages BEGIN
  INSERT INTO messages_fts(messages_fts, rowid, content)
    VALUES('delete', old.rowid, old.content);
END;
