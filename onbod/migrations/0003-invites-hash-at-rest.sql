-- Mirrors store/migrations/0077-invites-hash-at-rest.sql (I1: invites.token
-- was plaintext at rest; ref = hex(sha256(token)) becomes the primary key,
-- same as route_tokens' token_hash). Keeping the two in step is the point
-- (see store 0076's comment on the same discipline for user_profiles).
--
-- SQLite has no sha256(), so this migration only reshapes the table; onbod's
-- openOwnedDB calls store.BackfillInviteRefs immediately after running these
-- migrations to carry existing rows forward and drop invites_legacy.
ALTER TABLE invites RENAME TO invites_legacy;

CREATE TABLE invites (
  ref           TEXT PRIMARY KEY,
  target_glob   TEXT NOT NULL,
  issued_by_sub TEXT NOT NULL,
  issued_at     TEXT NOT NULL,
  expires_at    TEXT,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0
);
