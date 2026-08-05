-- Z3: onboarding.token was a live PLAINTEXT bearer (onbod mints it with
-- core.GenHexToken and mails it as /onboard?token=<raw>), so any read access to
-- the DB could redeem any pending admission — the same hole I1 closed for
-- invites (0077) and 0059 closed for route_tokens. Same treatment: only
-- hex(sha256(token)) is persisted, in token_ref; the raw bearer lives only in
-- the link the user was sent.
--
-- token_ref replaces token as the lookup key but NOT as a primary key: jid
-- stays the PK (unlike invites, where ref was already the public handle). NULL
-- token_ref keeps its existing meaning — "consumed or never minted".
--
-- SQLite has no sha256(), so this migration only reshapes the table; the data
-- carries forward via store.BackfillOnboardingTokenRefs (Go) immediately after
-- this runs — see store.migrate (store/store.go) and onbod/db.go's
-- openOwnedDB. Renamed, not dropped, so a crash between this migration and the
-- Go step leaves recoverable data instead of silently discarding every pending
-- admission link; the backfill drops onboarding_legacy once every row landed.
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
