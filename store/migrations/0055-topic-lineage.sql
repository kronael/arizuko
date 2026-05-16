-- v0.40.0: topic lineage — fork primitive + per-topic observed cursor.
-- Spec: specs/6/F-topic-lineage.md.
--
-- Three nullable columns on sessions. Format invariant: RFC3339Nano
-- UTC for every timestamp arizuko writes to the schema. Go layer
-- computes timestamps; no strftime in SQL (would diverge format).
-- All NULL on existing rows; ObservedSince treats NULL cursor as
-- "no lower bound; window-cap still applies" — zero behavior change
-- at migration. First turn after upgrade writes the cursor.

ALTER TABLE sessions ADD COLUMN parent_topic    TEXT;
ALTER TABLE sessions ADD COLUMN forked_at       TEXT;
ALTER TABLE sessions ADD COLUMN observed_cursor TEXT;

CREATE INDEX idx_sessions_lineage ON sessions(group_folder, parent_topic);
