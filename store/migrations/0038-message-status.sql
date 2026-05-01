-- Add a delivery-state column to messages so outbound rows can be
-- reconciled by polling instead of a synchronous in-memory callback.
--
-- Status values:
--   'sent'    — terminal state: row delivered (or never required delivery,
--               i.e. inbound). Default for backwards-compat with existing rows.
--   'pending' — outbound row queued; a poll loop will dispatch it.
--   'failed'  — terminal failure after a finite number of retries.
--
-- Existing rows are 'sent' (already delivered or inbound). Only outbound
-- bot-message inserts use 'pending' going forward; the gateway's poll
-- loop transitions them to 'sent' or 'failed'.
ALTER TABLE messages ADD COLUMN status TEXT NOT NULL DEFAULT 'sent';
CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status)
  WHERE status != 'sent';
