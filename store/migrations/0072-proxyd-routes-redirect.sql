-- A proxyd route may be a redirect instead of a proxy: a non-empty
-- `redirect_to` sends a 302 to the destination, and the route then carries no
-- backend.
--
-- `proxyd_routes` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER
-- to start carrying an attribute.
SELECT 1;
