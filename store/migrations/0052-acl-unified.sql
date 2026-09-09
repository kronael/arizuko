-- Unified ACL foundation (spec 6/9): one grant, addressed by the six fields
-- that ARE the grant — principal, action, scope, params, predicate and effect —
-- plus one membership edge, addressed by (child, parent).
--
-- Neither `acl` nor `acl_membership` is a table this chain creates: the driver
-- makes each one, from the declaration that sits beside the code writing it
-- (store.EnsureEntities, spec 6/37 step 7). An entity keeps the name of the
-- table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-routd` and refuses to open until it has been.
SELECT 1;
