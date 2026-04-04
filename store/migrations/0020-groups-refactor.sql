-- Refactor: rename registered_groups → groups, rekey by folder.
-- Move agent_cursor to chats. Create default routes for JID→folder mappings.

-- 1. Add agent_cursor column to chats
ALTER TABLE chats ADD COLUMN agent_cursor TEXT;

-- 2. Copy agent_cursor values from registered_groups into chats (upsert)
INSERT INTO chats (jid, agent_cursor)
  SELECT jid, agent_cursor FROM registered_groups WHERE agent_cursor IS NOT NULL AND agent_cursor != ''
  ON CONFLICT(jid) DO UPDATE SET agent_cursor = excluded.agent_cursor;

-- 3. Create default routes for each registered_groups JID→folder mapping
--    (only if no default route already exists for that JID)
INSERT OR IGNORE INTO routes (jid, seq, type, match, target)
  SELECT jid, 0, 'default', NULL, folder
  FROM registered_groups
  WHERE jid NOT IN (
    SELECT r.jid FROM routes r WHERE r.type = 'default' AND (r.match IS NULL OR r.match = '')
  );

-- 4. Create new groups table keyed by folder
CREATE TABLE groups (
  folder              TEXT PRIMARY KEY,
  name                TEXT NOT NULL,
  added_at            TEXT NOT NULL,
  container_config    TEXT,
  slink_token         TEXT,
  parent              TEXT,
  state               TEXT NOT NULL DEFAULT 'active',
  spawn_ttl_days      INTEGER NOT NULL DEFAULT 7,
  archive_closed_days INTEGER NOT NULL DEFAULT 1,
  updated_at          TEXT
);

-- 5. Populate groups from registered_groups
INSERT INTO groups (folder, name, added_at, container_config, slink_token, parent,
                    state, spawn_ttl_days, archive_closed_days, updated_at)
  SELECT folder, name, added_at, container_config, slink_token, parent,
         COALESCE(state, 'active'),
         COALESCE(spawn_ttl_days, 7),
         COALESCE(archive_closed_days, 1),
         updated_at
  FROM registered_groups;

-- 6. Drop old table
DROP TABLE registered_groups;
