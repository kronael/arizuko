-- v0.38.0: drop the ACL primitives the unified acl + acl_membership model
-- replaces. Spec: specs/6/9-acl-unified.md. Every authorization check reads
-- grants and membership edges; nothing reads these tables.
--
-- The conversion those tables feed writes items, not rows —
-- user_groups(sub, '**') becomes a membership of role:operator,
-- user_groups(sub, folder) an admin grant on the folder, user_jids(sub, jid) a
-- membership edge — and neither destination is a table this chain creates: the
-- driver makes each one, from the declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). So `arizuko migrate-routd` reads
-- these tables and writes the items, and store.SeedRoutdFloor writes the
-- role:operator root every instance is born with. grant_rules feeds nothing: it
-- was empty on all prod instances except a stale marinade row, and operators
-- rebuild via CLI / dashd UI.

DROP TABLE IF EXISTS user_groups;
DROP TABLE IF EXISTS user_jids;
DROP TABLE IF EXISTS grant_rules;
DROP TABLE IF EXISTS grants;
