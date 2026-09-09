-- Per-scope secrets: scope_kind = "folder" (path glob) or "user"
-- (auth_users.sub), sealed under the AES-256-GCM keyring routd holds.
--
-- `secrets` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7), addressed by (scope_kind,
-- scope_id, key). An entity keeps the name of the table it replaces, so a
-- database carrying rows here is moved by `arizuko migrate-routd` and refuses
-- to open until it has been.
SELECT 1;
