-- Add sticky routing columns for @group and #topic commands.
--
-- `chats` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the declaration that sits beside the code writing it
-- (store.EnsureEntities, spec 6/37 step 7). An item carries the attributes its
-- writer sets, so a pin that was two columns is two attributes and there is
-- nothing to add. A database carrying rows here is moved by
-- `arizuko migrate-hot` and refuses to open until it has been.
SELECT 1;
