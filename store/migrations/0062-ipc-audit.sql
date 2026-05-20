CREATE TABLE IF NOT EXISTS ipc_audit (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  ts      DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  folder  TEXT NOT NULL,
  sub     TEXT NOT NULL,
  tool    TEXT NOT NULL,
  params  TEXT NOT NULL,
  outcome TEXT NOT NULL
);
