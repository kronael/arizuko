-- runed.db initial schema (spec 5/P § runed.db schema). runed owns this
-- DB and its own migration sequence (service="runed"). Holds only
-- execution runtime state with no home in routd. Times are RFC3339 TEXT;
-- token-ref columns store an opaque ref/jti, never a raw token.

CREATE TABLE session_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  group_folder  TEXT NOT NULL,
  session_id    TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  ended_at      TEXT,
  result        TEXT,
  error         TEXT,
  message_count INTEGER
);
CREATE INDEX idx_session_log_folder ON session_log(group_folder, id DESC);

CREATE TABLE spawns (
  run_id         TEXT PRIMARY KEY,
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL,
  session_log_id INTEGER REFERENCES session_log(id),
  mcp_token_jti  TEXT,
  session_id     TEXT,
  state          TEXT NOT NULL,
  outcome        TEXT,
  exit_code      INTEGER,
  steered        INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  started_at     TEXT,
  ended_at       TEXT
);
CREATE INDEX idx_spawns_folder ON spawns(folder, created_at DESC);
CREATE INDEX idx_spawns_state ON spawns(state);

CREATE TABLE spawn_logs (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id  TEXT NOT NULL REFERENCES spawns(run_id) ON DELETE CASCADE,
  ts      TEXT NOT NULL,
  kind    TEXT NOT NULL,
  line    TEXT NOT NULL
);
CREATE INDEX idx_spawn_logs_run ON spawn_logs(run_id, id);

CREATE TABLE mcp_tokens (
  jti        TEXT PRIMARY KEY,
  run_id     TEXT NOT NULL UNIQUE REFERENCES spawns(run_id) ON DELETE CASCADE,
  parent_jti TEXT NOT NULL,
  folder     TEXT NOT NULL,
  scope      TEXT NOT NULL,
  issued_at  TEXT NOT NULL,
  expires_at TEXT NOT NULL
);
CREATE INDEX idx_mcp_tokens_expiry ON mcp_tokens(expires_at);
