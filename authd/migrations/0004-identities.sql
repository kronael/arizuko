-- authd owns cross-channel identity (spec 5/9): identities is a canonical user,
-- identity_claims links one or more platform sender subs to it, identity_codes
-- are short-lived link codes. Schema mirrors store/migrations/0035-identities.sql
-- verbatim so the same reader (store.GetIdentityForSub) runs against auth.db's
-- own rows instead of routd sibling-reading gated's messages.db. Advisory only —
-- agents query via inspect_identity, never enforce.
CREATE TABLE identities (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at TEXT NOT NULL
);

CREATE TABLE identity_claims (
  sub         TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  claimed_at  TEXT NOT NULL
);

CREATE INDEX idx_identity_claims_id ON identity_claims(identity_id);

CREATE TABLE identity_codes (
  code        TEXT PRIMARY KEY,
  identity_id TEXT NOT NULL,
  expires_at  TEXT NOT NULL
);
