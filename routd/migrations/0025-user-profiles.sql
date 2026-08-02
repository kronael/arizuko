-- routd.db's auth_users and authd's auth.db auth_users are different tables
-- that happened to share a name: authd's is keyed by user_id and anchors
-- oauth_identities; this one is keyed by sub and carries username,
-- linked_to_sub and the per-user spend cap. Rename ours so the collision
-- disappears; authd's is untouched.
--
-- hash is dead — no SELECT anywhere, written only to satisfy NOT NULL.
-- username STAYS: onbod writes it in createWorldTx and reads it on every
-- GET /onboard (a failed scan renders "User not found."). The spend cap STAYS
-- on this table: a separate table for one integer keyed by the same sub buys
-- nothing. Rows carry across untouched.
ALTER TABLE auth_users RENAME TO user_profiles;
ALTER TABLE user_profiles DROP COLUMN hash;

DROP INDEX IF EXISTS idx_auth_users_linked_to_sub;
CREATE INDEX idx_user_profiles_linked_to_sub
  ON user_profiles(linked_to_sub) WHERE linked_to_sub IS NOT NULL;
