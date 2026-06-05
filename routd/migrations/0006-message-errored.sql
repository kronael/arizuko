-- Per-message errored flag. A run that definitively fails marks its trigger
-- batch errored=1 (MarkMessagesErrored); the circuit breaker then prunes those
-- rows for the chat (DeleteErroredMessages) and clears the folder session,
-- mirroring gated's onCircuitBreakerOpen (gateway/gateway.go). The chats.errored
-- flag stays the coarse "this chat has seen a failure" signal for /status; this
-- column is the row-level prune target.

ALTER TABLE messages ADD COLUMN errored INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_messages_errored ON messages(chat_jid, errored) WHERE errored = 1;
