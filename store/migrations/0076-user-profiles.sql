-- Mirrors routd/migrations/0025: store's accessors (CreateAuthUser, UserCap,
-- CanonicalSub, …) name ONE table, and store's own schema is what every
-- OpenMem-backed test runs them against. Keeping the two in step is the point;
-- messages.db is frozen legacy and no live daemon reads this table there.
--
-- `arizuko migrate-split` still reads a PRE-split messages.db, where the table
-- predates this migration and is called auth_users — that copy spec keeps the
-- old source name and fails loudly if it is absent.
ALTER TABLE auth_users RENAME TO user_profiles;
ALTER TABLE user_profiles DROP COLUMN hash;

DROP INDEX IF EXISTS idx_auth_users_linked_to_sub;
CREATE INDEX idx_user_profiles_linked_to_sub
  ON user_profiles(linked_to_sub) WHERE linked_to_sub IS NOT NULL;
