-- Recreate sessions table with topic column and composite PK.
-- Existing rows migrated with topic=''.
--
-- The prefix-route uniqueness this file also declared is `routes`, an objstore
-- entity rather than a table this chain creates: the driver makes it, from the
-- one declaration that sits beside the code writing it (store.EnsureEntities,
-- spec 6/37 step 7). A rule is addressed by (seq, target, match), so the
-- coordinate IS the uniqueness a second insert of the same rule would have
-- collided with, and there is no index to declare.

CREATE TABLE sessions_new (
  group_folder TEXT NOT NULL,
  topic        TEXT NOT NULL DEFAULT '',
  session_id   TEXT NOT NULL,
  PRIMARY KEY (group_folder, topic)
);

INSERT INTO sessions_new (group_folder, topic, session_id)
  SELECT group_folder, '', session_id FROM sessions;

DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;

ALTER TABLE messages ADD COLUMN topic TEXT NOT NULL DEFAULT '';
