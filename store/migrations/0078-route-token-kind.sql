-- route-token kind (spec 5/31): 'route' = the delivery bearer, 'pair' = an
-- identity-pairing link. Resolution is kind-scoped in both directions, so
-- neither can be redeemed as the other. Rationale is in routd/migrations/0026.
--
-- `route_tokens` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER
-- to start carrying an attribute.
SELECT 1;
