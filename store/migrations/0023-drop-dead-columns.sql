-- Drop dead columns and rebuild the receive identity model around
-- messages.source as the canonical adapter-of-record per message.
--
-- Removed:
--   chats.name              — only ever written, never read
--   chats.channel           — pin-by-chat layer; replaced by per-message source
--   chats.is_group          — only ever written
--   chats.last_message_time — only ever written
--   messages.group_folder   — only ever written
--   onboarding.sender       — only ever written
--   onboarding.world_name   — only ever written
--   onboarding.channel      — replaced by /v1/outbound's LatestSource lookup
--
-- messages.source already exists (added in 0005). It is now repurposed
-- as the adapter that handled the row: receiver for inbound, empty
-- string for outbound.
--
-- The chats rebuild is gone with the table: `chats` is an objstore entity rather
-- than a table this chain creates (store.EnsureEntities, spec 6/37 step 7), and
-- an attribute an item does not carry is simply absent, so there are no dead
-- columns to drop.

CREATE TABLE messages_new (
  id              TEXT PRIMARY KEY,
  chat_jid        TEXT NOT NULL,
  sender          TEXT NOT NULL,
  sender_name     TEXT,
  content         TEXT NOT NULL,
  timestamp       TEXT NOT NULL,
  is_from_me      INTEGER NOT NULL DEFAULT 0,
  is_bot_message  INTEGER NOT NULL DEFAULT 0,
  forwarded_from  TEXT,
  reply_to_id     TEXT,
  reply_to_text   TEXT,
  reply_to_sender TEXT,
  topic           TEXT NOT NULL DEFAULT '',
  routed_to       TEXT NOT NULL DEFAULT '',
  verb            TEXT NOT NULL DEFAULT 'message',
  attachments     TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT ''
);
INSERT INTO messages_new
  (id, chat_jid, sender, sender_name, content, timestamp,
   is_from_me, is_bot_message, forwarded_from, reply_to_id,
   reply_to_text, reply_to_sender, topic, routed_to, verb, attachments, source)
  SELECT
    id, chat_jid, sender, sender_name, content, timestamp,
    COALESCE(is_from_me, 0), COALESCE(is_bot_message, 0),
    forwarded_from, reply_to_id, reply_to_text, reply_to_sender,
    COALESCE(topic, ''), COALESCE(routed_to, ''),
    COALESCE(verb, 'message'), COALESCE(attachments, ''),
    COALESCE(source, '')
  FROM messages;
DROP INDEX IF EXISTS idx_messages_chat_ts;
DROP TABLE messages;
ALTER TABLE messages_new RENAME TO messages;
CREATE INDEX idx_messages_chat_ts ON messages(chat_jid, timestamp);

-- The onboarding rebuild that dropped `sender`, `world_name` and `channel` is
-- gone with the table: an admission is an objstore item, and an attribute it
-- does not carry is simply absent.
