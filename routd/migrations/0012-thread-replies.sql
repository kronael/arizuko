-- Reply-into-thread: the reply path roots a new platform thread on the
-- turn's trigger message. trigger_msg_id records that message's platform id
-- at dispatch; groups.thread_replies is the per-group preference (NULL =
-- default: thread in multi-user chats, inline in DMs).
ALTER TABLE turn_context ADD COLUMN trigger_msg_id TEXT;
ALTER TABLE groups ADD COLUMN thread_replies INTEGER;
