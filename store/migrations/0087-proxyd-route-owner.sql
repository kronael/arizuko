-- proxyd_routes.owner says who may rewrite a route: 'arizuko' for the paths
-- `arizuko generate` ships, 'operator' for the ones declared through
-- /v1/proxyd_routes. Rationale is in routd/migrations/0038.
--
-- `proxyd_routes` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER
-- to start carrying an attribute.
SELECT 1;
