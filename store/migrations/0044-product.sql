-- A group's `product` names the persona bundle it runs, defaulting to
-- 'assistant'.
--
-- `groups` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7). An item needs no ALTER to start
-- carrying an attribute.
SELECT 1;
