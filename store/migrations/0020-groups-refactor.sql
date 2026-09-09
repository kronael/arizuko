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

-- 1. Add agent_cursor column to chats
ALTER TABLE chats ADD COLUMN agent_cursor TEXT;

-- 2. Copy agent_cursor values from registered_groups into chats (upsert)
INSERT INTO chats (jid, agent_cursor)
  SELECT jid, agent_cursor FROM registered_groups WHERE agent_cursor IS NOT NULL AND agent_cursor != ''
  ON CONFLICT(jid) DO UPDATE SET agent_cursor = excluded.agent_cursor;

-- 3. Drop the JID-keyed table
DROP TABLE registered_groups;
