-- Spec 5/9: cross-channel identity link.
-- identities is a canonical user; identity_claims links one or more
-- platform sender subs to it. identity_codes are short-lived link
-- codes minted by /auth/link-code and consumed by inbound message
-- detection in api/api.go.
--
-- Advisory only — agents query via inspect_identity, never enforce.
-- Coexists with user_jids (sub→folder onboarding binding); claims are
-- the orthogonal sub→identity merge.

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
