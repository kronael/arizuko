-- The engagement window on a (jid, topic) pair: when it ends and which folder
-- holds it.
--
-- `chat_reply_state` is an objstore entity rather than a table this chain
-- creates: the driver makes it, from the declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). Both are attributes, and
-- an ended window carries no deadline at all rather than a NULL one. A database
-- carrying rows here is moved by `arizuko migrate-hot` and refuses to open until
-- it has been.
SELECT 1;
