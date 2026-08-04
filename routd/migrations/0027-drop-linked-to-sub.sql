-- user_profiles.linked_to_sub was the third of three "one human, many logins"
-- models (BUGS P2). Nothing ever wrote it: the live account-link flow is
-- dashd's `?intent=link` → authd's OAuth dispatch → upsertOAuthUser, which
-- writes auth.db's auth_users + oauth_identities. So both readers — store's
-- CanonicalSub and dashd's linked-accounts list — always saw the empty answer.
-- Zero non-NULL values on every deployed instance.
--
-- Its accessors (CanonicalSub, LinkSubToCanonical, LinkedSubs, AuthUserBySub)
-- go with it; binding a channel identity to a person is spec 5/31 pairing,
-- which lives in acl_membership.
DROP INDEX IF EXISTS idx_user_profiles_linked_to_sub;
DROP INDEX IF EXISTS idx_auth_users_linked_to_sub;
ALTER TABLE user_profiles DROP COLUMN linked_to_sub;
