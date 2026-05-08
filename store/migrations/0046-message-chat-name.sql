-- Add human-readable channel/group name to messages.
-- Populated by adapters that have it at receive time (discd, teled).
-- Empty string for DMs and adapters that don't provide it.
ALTER TABLE messages ADD COLUMN chat_name TEXT NOT NULL DEFAULT '';
