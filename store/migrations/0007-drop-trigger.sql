-- Remove trigger_word and requires_trigger (dead code — replaced by TriggerRE @mention detection)
CREATE TABLE registered_groups_new (
  jid TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  folder TEXT NOT NULL,
  added_at TEXT NOT NULL,
  container_config TEXT,
  slink_token TEXT,
  parent TEXT,
  routing_rules TEXT,
  agent_cursor TEXT
);

INSERT INTO registered_groups_new
  SELECT jid, name, folder, added_at, container_config, slink_token, parent, routing_rules, agent_cursor
  FROM registered_groups;

DROP TABLE registered_groups;
ALTER TABLE registered_groups_new RENAME TO registered_groups;
