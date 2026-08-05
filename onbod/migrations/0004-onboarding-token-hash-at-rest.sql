-- Mirrors store/migrations/0080-onboarding-token-hash-at-rest.sql (Z3:
-- onboarding.token was a live plaintext bearer; token_ref = hex(sha256(token))
-- replaces it as the lookup key, same scheme as invites' ref and
-- route_tokens' token_hash). Keeping the two chains in step is the point —
-- same discipline as onbod 0003 / store 0077 for invites.
--
-- SQLite has no sha256(), so this migration only reshapes the table; onbod's
-- openOwnedDB calls store.BackfillOnboardingTokenRefs immediately after
-- running these migrations to carry existing rows forward (hashing each live
-- token so already-sent links keep working) and drop onboarding_legacy.
ALTER TABLE onboarding RENAME TO onboarding_legacy;

CREATE TABLE onboarding (
  jid           TEXT PRIMARY KEY,
  status        TEXT NOT NULL,
  prompted_at   TEXT,
  created       TEXT NOT NULL,
  token_ref     TEXT,
  token_expires TEXT,
  user_sub      TEXT,
  gate          TEXT,
  queued_at     TEXT,
  admitted_at   TEXT
);

CREATE INDEX IF NOT EXISTS idx_onboarding_token_ref ON onboarding(token_ref);
