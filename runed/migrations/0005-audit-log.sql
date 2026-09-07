-- runed owns runed.db and records into it. audit_log is an objstore entity
-- rather than a table this chain creates: the driver makes it from
-- audit.EnsureEntities (spec 6/37 step 7), and `arizuko migrate-audit` moves a
-- database that carries rows.
SELECT 1;
