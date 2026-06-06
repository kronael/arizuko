-- onbod owns the onboarding admission state machine + invite links + the
-- per-gate daily admission limits (spec 5/5 § Daemon ownership). In the split
-- topology these three tables move OUT of gated's messages.db into onbod's own
-- onbod.db, so the writers (dashd invites, CLI `arizuko invite`/`gate`, routd's
-- /invite + /gate commands) reach them through onbod instead of sibling-writing
-- messages.db. Schemas mirror the FINAL messages.db shape so store.New(onbod.db)
-- reuses gated's exact readers/writers verbatim:
--   onboarding       — store 0009 created it; 0023 trimmed it; 0024 added
--                      token/token_expires/user_sub + idx_onboarding_token;
--                      0027 added gate/queued_at; 0071 added admitted_at.
--   invites          — store 0032 (rewrite of 0028 invitations).
--   onboarding_gates — store 0029.
CREATE TABLE onboarding (
  jid           TEXT PRIMARY KEY,
  status        TEXT NOT NULL,
  prompted_at   TEXT,
  created       TEXT NOT NULL,
  token         TEXT,
  token_expires TEXT,
  user_sub      TEXT,
  gate          TEXT,
  queued_at     TEXT,
  admitted_at   TEXT
);
CREATE INDEX idx_onboarding_token ON onboarding(token);

CREATE TABLE invites (
  token         TEXT PRIMARY KEY,
  target_glob   TEXT NOT NULL,
  issued_by_sub TEXT NOT NULL,
  issued_at     TEXT NOT NULL,
  expires_at    TEXT,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE onboarding_gates (
  gate          TEXT PRIMARY KEY,
  limit_per_day INTEGER NOT NULL,
  enabled       INTEGER NOT NULL DEFAULT 1
);
