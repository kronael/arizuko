-- Stamp every outbound assistant message with the turn (= inbound message id)
-- that produced it. Lets the slink round-handle protocol fetch all frames from
-- a single run by foreign-key lookup instead of time-window correlation.
ALTER TABLE messages ADD COLUMN turn_id TEXT;
CREATE INDEX idx_messages_turn_id ON messages(turn_id) WHERE turn_id IS NOT NULL;
