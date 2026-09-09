-- messages.errored: the row-level prune target the circuit breaker clears.
--
-- The `chats.errored` column this file also dropped is an attribute now:
-- `chats` is an objstore entity rather than a table this chain creates
-- (store.EnsureEntities, spec 6/37 step 7), and an attribute an item does not
-- carry is absent, so there is no column to drop.
ALTER TABLE messages ADD COLUMN errored INTEGER NOT NULL DEFAULT 0;
