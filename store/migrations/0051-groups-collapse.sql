-- v0.37.0: path is identity. Drop `parent` and `name` from groups.
-- Derive parent via filepath.Dir(folder), display via filepath.Base(folder).
--
-- Operators with legacy flat-slug rows (slug not matching parent path)
-- must rename on-disk dirs + aux-table refs manually before this migration
-- runs. See specs/6/X-groups-collapse.md for the brute-force shell snippet.

CREATE TABLE groups_new (
  folder                 TEXT PRIMARY KEY,
  added_at               TEXT NOT NULL,
  container_config       TEXT,
  slink_token            TEXT,
  updated_at             TEXT,
  product                TEXT NOT NULL DEFAULT 'assistant',
  cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0
);

INSERT INTO groups_new (folder, added_at, container_config, slink_token,
                        updated_at, product, cost_cap_cents_per_day)
SELECT folder, added_at, container_config, slink_token,
       updated_at, product, cost_cap_cents_per_day
FROM groups;

DROP TABLE groups;
ALTER TABLE groups_new RENAME TO groups;
