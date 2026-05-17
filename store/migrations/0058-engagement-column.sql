ALTER TABLE chat_reply_state ADD COLUMN engaged_until TEXT;
ALTER TABLE chat_reply_state ADD COLUMN engaged_folder TEXT NOT NULL DEFAULT '';
