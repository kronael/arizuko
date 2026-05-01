-- Drop group state machinery: groups exist until explicitly removed.
-- No active/closed/archived states; no auto-archive; no soft-delete.
-- registered_groups was dropped in 0020; only groups remains.

ALTER TABLE groups DROP COLUMN state;
ALTER TABLE groups DROP COLUMN spawn_ttl_days;
ALTER TABLE groups DROP COLUMN archive_closed_days;
