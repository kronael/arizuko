-- route-token link context (spec 5/W § link context): a token optionally
-- carries issuer-authored instructions on how to process the data received
-- through its URL. Rationale is in routd/migrations/0018.
--
-- `route_tokens` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER
-- to start carrying an attribute.
SELECT 1;
