-- Mirrors onbod/migrations/0005-invite-redemptions.sql (BUGS F50). Same
-- discipline as 0077/0080 and their onbod 0003/0004 twins: onbod owns the
-- table, and this chain is kept in step so a store.Migrate'd DB has the same
-- shape as a live onbod.db.
--
-- Rationale for the table is in onbod's copy. Short version: a subgroup invite
-- grants no acl row at redemption, so the parent folder it authorizes had no
-- record and handleCreateWorld took it from the `pending_target` cookie —
-- forgeable by the caller into any tenant's subtree.
CREATE TABLE IF NOT EXISTS invite_redemptions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_sub    TEXT NOT NULL,
  target_glob TEXT NOT NULL,
  redeemed_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_invite_redemptions_user
  ON invite_redemptions(user_sub);
