-- route-token link context (spec 5/W § link context): a token optionally
-- carries issuer-authored instructions on how to process the data received
-- through its URL. An absent `context` attribute is a token that carries none,
-- and behavior is unchanged.
--
-- `route_tokens` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7), addressed by
-- hex(sha256(token)). An item needs no ALTER to start carrying an attribute.
SELECT 1;
