-- F1 (adversary audit 2026-07-30): role:operator is the delegation root (spec 4/R)
-- but routd.db never seeds its base row — routd migrations 0001-0021 only CREATE the
-- acl table + ADD grant_option; the base (*,**) row lives only in messages.db
-- (store/0053). So on a FRESH split install routd.db has no role:operator row at all,
-- and on a MIGRATE-SPLIT copyInto omits grant_option → the copied row lands at 0.
-- Either way auth.Delegate's grantableRows('role:operator') is empty and the root can
-- delegate nothing once wired. Seed it idempotently, WITH GRANT OPTION.
--
-- Ordering-safe: migrate_split runs this (routd.Open) BEFORE copyInto, so INSERT OR
-- IGNORE seeds the row first and the later copy's INSERT OR IGNORE is a no-op that
-- preserves grant_option=1; on a fresh install it seeds directly; on an already-split
-- instance the row exists so the UPDATE fixes its grant_option.
INSERT OR IGNORE INTO acl
  (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option)
  VALUES ('role:operator', '*', '**', 'allow', '', '', 'system', '2026-07-30T00:00:00Z', 1);
UPDATE acl SET grant_option = 1 WHERE principal = 'role:operator' AND action = '*';
