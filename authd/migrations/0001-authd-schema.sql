-- authd owns these tables. Keyed service="authd" in the shared `migrations`
-- table, so this 0001 is independent of store's 0001..NNNN sequence
-- (db_utils.Migrate PK is (service, version)). Spec: specs/5/1-auth-standalone.md.

-- signing_keys: ES256 keypairs authd holds. TIME-BASED validity, no `revoked`
-- column. A key serves while it is `active` OR `now < retired_at +
-- max_access_ttl` (a retired key still verifies tokens it signed before
-- retirement, until those tokens' max TTL elapses). Emergency revoke =
-- backdate retired_at into the past so the serving window closes now.
-- priv_pem holds the PKCS#8 private key; pub_pem its SPKI public half.
CREATE TABLE signing_keys (
  kid         TEXT PRIMARY KEY,
  priv_pem    TEXT NOT NULL,
  pub_pem     TEXT NOT NULL,
  active      INTEGER NOT NULL DEFAULT 1,  -- 1 = the current signing key
  created_at  TEXT NOT NULL,
  retired_at  TEXT                          -- NULL while active; set on rotation/revoke
);

-- auth_users: one canonical user. sub is stored BARE (no "user:" prefix);
-- the prefix is added only in the minted claim.
CREATE TABLE auth_users (
  user_id    TEXT PRIMARY KEY,
  name       TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL
);

-- oauth_identities: provider logins linked to a canonical user. One user may
-- link many providers, but a given (user, provider) pair is unique.
-- provider_sub is the provider's stable id (GitHub: the numeric account id,
-- never the renamable login).
CREATE TABLE oauth_identities (
  user_id      TEXT NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  provider     TEXT NOT NULL,
  provider_sub TEXT NOT NULL,
  linked_at    TEXT NOT NULL,
  UNIQUE(user_id, provider),
  UNIQUE(provider, provider_sub)
);

-- refresh_tokens: rotating refresh tokens. token_hash = sha256(token).
-- family_id groups a rotation lineage; presenting an already-used token is a
-- reuse attack -> the whole family is revoked. used_at is the tombstone that
-- makes reuse detectable (we do NOT delete on use).
CREATE TABLE refresh_tokens (
  token_hash TEXT PRIMARY KEY,
  family_id  TEXT NOT NULL,
  sub        TEXT NOT NULL,
  scope      TEXT NOT NULL DEFAULT '',  -- comma-separated; bounds the next access mint
  aud        TEXT NOT NULL DEFAULT '',
  issued_at  TEXT NOT NULL,
  expires_at TEXT NOT NULL,
  used_at    TEXT,                       -- NULL until rotated; non-NULL = spent
  revoked_at TEXT                        -- non-NULL = family killed
);

CREATE INDEX idx_refresh_family ON refresh_tokens(family_id);
