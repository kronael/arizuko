-- Add spawn lifecycle state to groups table
ALTER TABLE registered_groups ADD COLUMN state TEXT NOT NULL DEFAULT 'active';
ALTER TABLE registered_groups ADD COLUMN spawn_ttl_days INTEGER NOT NULL DEFAULT 7;
ALTER TABLE registered_groups ADD COLUMN archive_closed_days INTEGER NOT NULL DEFAULT 1;
ALTER TABLE registered_groups ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
-- state: active | closed | archived
