-- Grants table and audit log columns on messages

CREATE TABLE IF NOT EXISTS grants (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  jid TEXT NOT NULL,
  role TEXT NOT NULL,
  granted_by TEXT,
  granted_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_grants_jid ON grants(jid);

ALTER TABLE messages ADD COLUMN source TEXT;
ALTER TABLE messages ADD COLUMN group_folder TEXT;
