-- audit_log is an objstore entity, not a table this chain creates: the driver
-- makes it, from the one declaration that sits beside the code writing it
-- (audit.EnsureEntities, spec 6/37 step 7). An entity keeps the name of the
-- table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-audit` and refuses to open until it has been.
--
-- ipc_audit and cli_audit are consolidated into it: both are small, recent and
-- have no consumer beyond the audit trail itself.
SELECT 1;
