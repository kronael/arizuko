-- Add routed_to column for reply-chain group routing
ALTER TABLE messages ADD COLUMN routed_to TEXT NOT NULL DEFAULT '';
