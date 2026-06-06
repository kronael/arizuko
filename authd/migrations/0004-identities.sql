-- authd owns cross-channel identity (spec 5/9): identities is a canonical user,
-- identity_claims links one or more platform sender subs to it, identity_codes
-- are short-lived link codes. Schema mirrors store/migrations/0035-identities.sql
-- verbatim so the same reader (store.GetIdentityForSub) runs against auth.db's
-- own rows instead of routd sibling-reading gated's messages.db. Advisory only —
-- agents query via inspect_identity, never enforce.
-- IF NOT EXISTS: `arizuko migrate-split` pre-creates these tables in auth.db
-- (authdIdentitySchema) without recording a migrations row, so this migration
-- re-runs on the next authd boot. Idempotent CREATEs make that a no-op.
CREATE TABLE IF NOT EXISTS identities (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS identity_claims (
  sub         TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  claimed_at  TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_identity_claims_id ON identity_claims(identity_id);

CREATE TABLE IF NOT EXISTS identity_codes (
  code        TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);
