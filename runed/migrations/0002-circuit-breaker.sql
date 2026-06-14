-- Circuit breaker state: per-folder consecutive failure count persisted so
-- runed survives restarts. The spec puts this as a column on spawns OR a
-- separate table; a separate table is cleaner (one row per folder, updated
-- atomically, no need to scan spawns for "most recent failure count").

CREATE TABLE circuit_breaker (
  folder        TEXT PRIMARY KEY,
  failures      INTEGER NOT NULL DEFAULT 0,
  last_failure  TEXT
);
