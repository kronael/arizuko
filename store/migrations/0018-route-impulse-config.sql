-- Route mode rides on the target as a URI fragment (0054), so a rule carries no
-- separate impulse configuration.
--
-- `routes` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER to start
-- carrying an attribute, and an attribute an item does not carry is simply
-- absent — the only null this model has.
SELECT 1;
