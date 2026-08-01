-- 4/R unified evaluator: collapse the four per-depth tier bundles (role:tier0..3)
-- to ONE seeded floor role, role:member — the messaging verbs only. Everything
-- above the floor (register_group, routes, network_*, schedule_*, observe_*,
-- invite_*, token mint, acl, egress, web:publish) is now EXPLICIT delegation from a
-- lineage ancestor or the operator's root grant (role:operator, seeded in 0022).
--
-- DEMOTION (intended, documented): every existing folder that sat above the floor
-- by DEPTH drops to role:member. A top-level world no longer gets management by
-- being top-level; a sub-folder no longer loses tools by being deep. Authority is
-- exactly what a folder's lineage delegated. After deploy the operator re-delegates
-- management (add_acl / delegate_group under /root) to the folders that need it.

-- 1. Seed role:member — the 12 messaging verbs, scope ** (containment is authorizeJID
--    route-ownership at runtime, not the acl scope). grant_option=0: the floor is not
--    re-delegable (every child is born a member directly).
INSERT OR IGNORE INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option) VALUES
  ('role:member', 'mcp:reply',      '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:send',       '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:send_file',  '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:send_voice', '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:post',       '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:forward',    '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:quote',      '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:repost',     '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:like',       '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:dislike',    '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:edit',       '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0),
  ('role:member', 'mcp:delete',     '**', 'allow', '', '', 'system:4r', '2026-08-01T00:00:00Z', 0);

-- 2. Reassign every folder membership from a tier bundle to role:member (idempotent),
--    then drop the tier edges.
INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at)
  SELECT child, 'role:member', 'system:4r-member', '2026-08-01T00:00:00Z'
    FROM acl_membership WHERE parent LIKE 'role:tier%';
DELETE FROM acl_membership WHERE parent LIKE 'role:tier%';

-- 3. Drop the tier-bundle role rows and every 4/R backfill (containment) row — no
--    inert rows left behind.
DELETE FROM acl WHERE principal LIKE 'role:tier%';
DELETE FROM acl WHERE granted_by = 'system:backfill-4r';
