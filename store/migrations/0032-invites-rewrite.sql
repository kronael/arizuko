-- Hard cutover: invitations → invites with the new schema.
-- Existing rows migrate forward atomically; no alias view, no backwards compat.
-- folder → target_glob (exact-match is a valid glob).

CREATE TABLE invites (
  token         TEXT PRIMARY KEY,
  target_glob   TEXT NOT NULL,
  issued_by_sub TEXT NOT NULL,
  issued_at     TEXT NOT NULL,
  expires_at    TEXT,
  max_uses      INTEGER NOT NULL DEFAULT 1,
  used_count    INTEGER NOT NULL DEFAULT 0
);

INSERT INTO invites (token, target_glob, issued_by_sub, issued_at, expires_at, max_uses, used_count)
SELECT token, folder, created_by, created_at, expires, max_uses, uses
FROM invitations;

DROP TABLE invitations;
