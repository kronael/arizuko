-- Add verb column for event type classification (message, join, edit, delete, etc.)
ALTER TABLE messages ADD COLUMN verb TEXT NOT NULL DEFAULT '';
