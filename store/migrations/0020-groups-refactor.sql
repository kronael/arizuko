-- Move agent_cursor onto chats, and retire registered_groups.
--
-- A group is an objstore entity — `groups`, addressed by its folder, made by
-- the driver from the one declaration that sits beside the code writing it
-- (store.EnsureEntities, spec 6/37 step 7) — and so is the default `routes`
-- rule that carries each JID to its folder. Neither is a table this chain
-- creates, so the folder-keyed group set and the routing rules a pre-move
-- database's registered_groups implies are written by `arizuko migrate-routd`,
-- which reads that table and writes the items. An entity keeps the name of the
-- table it replaces, and the database refuses to open until it has been moved.
--
-- `chats` is one as well, so the dispatch cursor this file moved onto it is an
-- attribute rather than a column: there is nothing to add and nothing to copy,
-- and `arizuko migrate-hot` carries the cursor from the table this chain left
-- behind.

DROP TABLE registered_groups;
