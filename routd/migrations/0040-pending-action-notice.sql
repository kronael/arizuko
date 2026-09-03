-- The platform id of the delivered notice, so a REACTION on it resolves the
-- call. A reaction is the one answer surface every chat platform has, and it
-- arrives naming the message it targets — without this column that id resolves
-- to nothing, because the notice is delivered straight to the adapter and never
-- becomes a `messages` row.
ALTER TABLE pending_actions ADD COLUMN notice_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS pending_actions_by_notice
  ON pending_actions(chat_jid, notice_id, status);
