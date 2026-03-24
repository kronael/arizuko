-- Add sticky routing columns for @group and #topic commands
ALTER TABLE chats ADD COLUMN sticky_group TEXT;
ALTER TABLE chats ADD COLUMN sticky_topic TEXT;
