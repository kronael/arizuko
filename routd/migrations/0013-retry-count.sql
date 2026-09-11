-- Turn retry: reschedule on SIGKILL/OOM/timeout. The turn's retry count tracks
-- attempts; a turn without a reply increments and requeues until MAX_TURN_RETRY.
--
-- `turn_context` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the declaration that sits beside the code writing it
-- (store.EnsureEntities, spec 6/37 step 7), and an item needs no ALTER to start
-- carrying an attribute. An entity keeps the name of the table it replaces, so a
-- database carrying rows here is moved by `arizuko migrate-hot` and refuses to
-- open until it has been.
SELECT 1;
