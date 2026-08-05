-- I1: invites.token was a bare TEXT PRIMARY KEY — plaintext at rest, so any
-- read access to the DB (every daemon, per BUGS A1) could redeem any pending
-- invite. route_tokens (0059) already does this correctly: the raw token is
-- returned once at issue and only its hash is persisted, looked up by hash.
-- Follow it exactly, but key by `ref` (store.TokenRef = hex(sha256(token)))
-- rather than a second raw hash column — ref is ALREADY the non-secret handle
-- every read surface hands out (205533dc), so it doubles as the hash-at-rest
-- primary key instead of adding a second identity value.
--
-- SQLite has no sha256(), so this migration only reshapes the table; the data
-- carries forward via store.BackfillInviteRefs (Go, sha256 in application
-- code) immediately after this runs — see store.migrate (store/store.go) and
-- onbod/db.go's openOwnedDB. Renamed, not dropped, so a crash between this
-- migration and the Go step leaves recoverable data instead of silently
-- discarding it; BackfillInviteRefs drops invites_legacy once every row has
-- carried forward.
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
