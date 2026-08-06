-- 5/33 floor backfill. Migration 0023 seeded the role:member GRANT rows and then
-- reassigned membership with
--   SELECT child, 'role:member' ... WHERE parent LIKE 'role:tier%'
-- which assumed folders were members of a tier bundle. They were not: the tier was
-- DERIVED (min(folder depth, 3)), never a stored acl_membership row. So the SELECT
-- matched nothing, the derived default was deleted with tiers, and every existing
-- folder came out a member of nothing.
--
-- Effect on a deployed instance: auth.EffectiveActions("folder:<f>") returns empty,
-- so routd's per-turn Visible view advertises ZERO MCP tools. The agent could still
-- answer — prose rides submit_turn, not the reply tool — which is why this read as
-- "the agent works" while send_file, edit, like and the rest were simply absent.
-- Verified on krons: folder:krons held mcp:reply/send/send_file/send_voice/edit all
-- false, while the operator principal held them via role:operator.
--
-- New groups are unaffected: PutGroup → assignDefaultRole (routd/seed_grants.go)
-- writes the edge at creation. This backfills the folders that predate that path.
INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at)
  SELECT 'folder:' || folder, 'role:member', 'system:4r-backfill', '2026-08-06T00:00:00Z'
    FROM groups;
