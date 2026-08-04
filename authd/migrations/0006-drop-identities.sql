-- identities/identity_claims was the advisory cross-channel identity model
-- (spec 5/9). It had a read surface but no writer authd ever ran: the live OAuth
-- login path writes auth_users(user_id) + oauth_identities, and GET
-- /v1/identities/{sub} was re-pointed at those (90e46d62). The only remaining
-- writer was `arizuko identity link`, which wrote rows nothing read.
--
-- Zero rows on every deployed instance. The manual bind it nominally offered is
-- what spec 5/31 delivers with consent (issue_pairing_link → acl_membership,
-- unpair to undo).
--
-- Dropped additively rather than by editing 0004, which has already run.
DROP INDEX IF EXISTS idx_identity_claims_id;
DROP TABLE IF EXISTS identity_claims;
DROP TABLE IF EXISTS identities;
