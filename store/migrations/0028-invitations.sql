CREATE TABLE IF NOT EXISTS invitations (
    token      TEXT PRIMARY KEY,
    folder     TEXT NOT NULL,
    created_by TEXT NOT NULL,
    created_at TEXT NOT NULL,
    uses       INTEGER NOT NULL DEFAULT 0,
    max_uses   INTEGER NOT NULL DEFAULT 1,
    expires    TEXT
);
