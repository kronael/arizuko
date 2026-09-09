-- routes.added_by / added_via (spec 5/18 step 7): a route records WHO exercised
-- routing authority over its target and BY WHICH ACT. Rationale is in
-- routd/migrations/0028.
--
-- `routes` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER to start
-- carrying an attribute.
SELECT 1;
