-- A group exists until it is explicitly removed: no active/closed/archived
-- state, no auto-archive, no soft-delete, so nothing carries state,
-- spawn_ttl_days or archive_closed_days.
--
-- `groups` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). An attribute an item does not
-- carry is simply absent, so there is no column to drop.
SELECT 1;
