-- Spec 6/2 Phase-3: runtime-mutable proxyd routes. Mirror of the in-memory
-- `Route` struct (proxyd/routes.go). proxyd seeds this table from
-- PROXYD_ROUTES_JSON on first boot when the table is empty; thereafter
-- the table is authoritative and PROXYD_ROUTES_JSON is ignored.
CREATE TABLE IF NOT EXISTS proxyd_routes (
  path             TEXT PRIMARY KEY,
  backend          TEXT NOT NULL,
  auth             TEXT NOT NULL,                  -- 'public' | 'user' | 'operator'
  gated_by         TEXT NOT NULL DEFAULT '',
  preserve_headers TEXT NOT NULL DEFAULT '[]',     -- JSON array
  strip_prefix     INTEGER NOT NULL DEFAULT 0      -- 0/1
);
