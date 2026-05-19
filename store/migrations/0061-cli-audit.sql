CREATE TABLE IF NOT EXISTS cli_audit (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts        DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  os_user   TEXT NOT NULL,
  command   TEXT NOT NULL,
  args      TEXT NOT NULL
);
