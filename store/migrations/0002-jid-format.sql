-- Normalize JID format: add channel prefix where missing.
--
-- The chat rewrites are gone with the table: `chats` is an objstore entity
-- rather than a table this chain creates (store.EnsureEntities, spec 6/37
-- step 7), and the messages rewrite below keyed off `chats.channel`, which is an
-- attribute now. This chain only ever runs whole against an EMPTY database, so
-- every UPDATE in it is a no-op there; a database that carried these rows
-- recorded this version years before the move, and `arizuko migrate-hot` reads
-- the table this chain left behind.

UPDATE registered_groups SET jid = 'telegram:' || jid
  WHERE jid GLOB '[0-9]*';
