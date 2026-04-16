-- Track when each grant was created (CLI `arizuko group ... grant`).
-- Nullable: rows inserted before this migration have no recorded timestamp.
ALTER TABLE user_groups ADD COLUMN granted_at TEXT;
