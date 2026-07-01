-- proxyd's login decision straddled two DBs: it read auth_sessions +
-- proxyd_routes from messages.db (store.Open) but users/scopes from
-- routd.db (store.OpenRoutd). The split cutover (2026-06-07) left
-- messages.db as a live 8th DB purely for these reads. Move both tables
-- into routd.db (the owner proxyd already opens for auth_users/acl/
-- route_tokens) so login reads ONE database.
--
-- Schema mirrors store/migrations exactly:
--   auth_sessions   <- store/0001-initial-schema.sql
--   proxyd_routes   <- store/0050-proxyd-routes.sql + 0072 redirect_to
--
-- Existing rows are copied from the sibling messages.db by routd.Open
-- (copyLegacyProxydTables) in autocommit AFTER migrations — ATTACH can't
-- run inside the migration runner's BEGIN EXCLUSIVE. The messages.db
-- copies stay intact as a one-release fallback (no DROP here).
CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_sub   TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS proxyd_routes (
  path             TEXT PRIMARY KEY,
  backend          TEXT NOT NULL,
  auth             TEXT NOT NULL,                  -- 'public' | 'user' | 'operator'
  gated_by         TEXT NOT NULL DEFAULT '',
  preserve_headers TEXT NOT NULL DEFAULT '[]',     -- JSON array
  strip_prefix     INTEGER NOT NULL DEFAULT 0,     -- 0/1
  redirect_to      TEXT NOT NULL DEFAULT ''
);
