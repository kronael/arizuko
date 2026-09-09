-- Spec 9/11: the sealed blob is the item's `value` attribute — one name, so the
-- writer and the broker never disagree about which one holds the secret.
--
-- `secrets` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). Renaming an attribute a pre-move
-- database wrote under another name is `arizuko migrate-routd`'s job, which is
-- the only reader of the old column.
SELECT 1;
