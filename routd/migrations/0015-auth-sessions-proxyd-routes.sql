-- auth_sessions: proxyd's login cookie. proxyd reads the session, the user and
-- the user's grants from ONE database — routd.db, the owner it already opens
-- for auth_users, acl and route_tokens — so a login decision never straddles
-- two files.
--
-- `proxyd_routes`, the other half of that decision, is an objstore entity
-- rather than a table this chain creates: the driver makes it, from the one
-- declaration that sits beside the code writing it (store.EnsureEntities, spec
-- 6/37 step 7), addressed by its path. An entity keeps the name of the table it
-- replaces, so a database carrying rows here is moved by
-- `arizuko migrate-routd` and refuses to open until it has been.
CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash TEXT PRIMARY KEY,
  user_sub   TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  created_at TEXT NOT NULL
);
