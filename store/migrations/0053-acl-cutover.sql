-- v0.38.0: cut over from legacy ACL primitives (user_groups, user_jids,
-- grant_rules, dead `grants` table) to the unified acl + acl_membership
-- schema seeded in 0052.
--
-- Spec: specs/6/9-acl-unified.md. End state: every authorization check
-- reads from acl + acl_membership; the legacy tables are dropped.
--
-- Conversion rules:
--   user_groups(sub, '**')        -> acl_membership(sub, role:operator)
--   user_groups(sub, folder)      -> acl(sub, admin, folder)
--   user_jids(sub, jid)           -> acl_membership(jid, sub)
--   grant_rules                   -> dropped; was empty on all prod
--                                    instances except a stale marinade row.
--                                    Operators rebuild via CLI / dashd UI.

-- Seed the operator role grant idempotently.
INSERT OR IGNORE INTO acl
  (principal, action, scope, effect, params, predicate, granted_at, granted_by)
VALUES
  ('role:operator', '*', '**', 'allow', '', '', datetime('now'), 'migration-0053');

-- Convert operator user_groups rows (folder='**') into role memberships.
INSERT OR IGNORE INTO acl_membership (child, parent, added_at, added_by)
SELECT user_sub, 'role:operator',
       COALESCE(granted_at, datetime('now')), 'migration-0053'
FROM user_groups
WHERE folder = '**';

-- Convert non-operator user_groups rows into admin grants on the folder.
INSERT OR IGNORE INTO acl
  (principal, action, scope, effect, params, predicate, granted_at, granted_by)
SELECT user_sub, 'admin', folder, 'allow', '', '',
       COALESCE(granted_at, datetime('now')), 'migration-0053'
FROM user_groups
WHERE folder != '**';

-- Convert user_jids into membership edges (jid IS-A canonical sub).
INSERT OR IGNORE INTO acl_membership (child, parent, added_at, added_by)
SELECT jid, user_sub,
       COALESCE(claimed, datetime('now')), 'migration-0053'
FROM user_jids;

-- Drop legacy tables.
DROP TABLE IF EXISTS user_groups;
DROP TABLE IF EXISTS user_jids;
DROP TABLE IF EXISTS grant_rules;
DROP TABLE IF EXISTS grants;
