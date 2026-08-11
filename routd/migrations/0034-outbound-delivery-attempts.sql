-- Outbound delivery bound. delivery_attempts counts the failed delivery
-- attempts the retry sweep made on a pending bot row. The sweep dead-letters
-- the row (status='failed') when the count reaches MAX_OUTBOUND_RETRY.
-- Every existing row starts at 0, so a live pending row keeps its full budget.
ALTER TABLE messages ADD COLUMN delivery_attempts INTEGER NOT NULL DEFAULT 0;
