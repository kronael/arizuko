-- route_tokens.target_folder — the folder the token DELIVERS to (spec 5/W).
-- owner_folder bounds revocation and may differ from the delivery target, so it
-- cannot answer "where does this payload go". Rationale is in
-- routd/migrations/0035.
--
-- `route_tokens` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER
-- to start carrying an attribute.
SELECT 1;
