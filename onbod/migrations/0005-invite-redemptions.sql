-- BUGS F50 — the parent folder a subgroup invite authorizes had no record.
--
-- A subgroup invite (target_glob ending in `/`) grants NOTHING at redemption:
-- store.consumeInvite writes the acl admin row only for non-slash targets,
-- because the folder does not exist until the username picker names it. So the
-- one caller who legitimately needs a parent holds no acl row, and there is
-- nothing for auth.Authorize to evaluate.
--
-- handleCreateWorld therefore took the parent from the `pending_target` cookie
-- and used it verbatim. The cookie is HttpOnly, which stops page JS — not the
-- caller, who owns the browser. An authenticated holder of no grant anywhere
-- posting `Cookie: pending_target=victim/` created `victim/pwned` and took
-- `victim/pwned/**` admin.
--
-- This table is the missing fact. consumeInvite writes one row per subgroup
-- redemption inside its existing transaction; handleCreateWorld DERIVES the
-- parent from it and deletes it on success, so one redemption is one world.
-- The cookie is gone: nothing the browser sends names a parent any more.
--
-- No expiry column. The cookie's MaxAge=600 was a lockout, not a control — an
-- invite consumed by a user who took eleven minutes to choose a username was
-- burned with the authority already expired.
CREATE TABLE IF NOT EXISTS invite_redemptions (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  user_sub    TEXT NOT NULL,
  target_glob TEXT NOT NULL,
  redeemed_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_invite_redemptions_user
  ON invite_redemptions(user_sub);
