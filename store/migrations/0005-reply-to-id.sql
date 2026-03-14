-- Add reply_to_id to messages

ALTER TABLE messages ADD COLUMN reply_to_id TEXT;
