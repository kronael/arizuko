-- routd owns routd.db and records into it, as do dashd, webd and proxyd, which
-- hold the file without opening a log of their own. audit_log is an objstore
-- entity rather than a table this chain creates: the driver makes it from the
-- one declaration beside the code that writes it (audit.EnsureEntities, spec
-- 6/37 step 7), and `arizuko migrate-audit` moves a database that carries rows.
SELECT 1;
