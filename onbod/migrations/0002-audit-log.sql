-- onbod owns onbod.db and records into it: the invite and gate writers put one
-- row inside the same transaction as the mutation, so this file must have
-- somewhere to write or the mutation rolls back with it. audit_log is an
-- objstore entity rather than a table this chain creates — the driver makes it
-- from audit.EnsureEntities (spec 6/37 step 7).
SELECT 1;
