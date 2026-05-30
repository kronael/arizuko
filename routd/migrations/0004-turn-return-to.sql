-- Delegation return-path (gated processSenderBatch parity). A delegated turn's
-- trigger batch carries forwarded_from = the origin chat JID; reply/send/
-- document delivery must return to the ORIGIN chat, not the child folder JID
-- the run addresses. Persist that return address on the turn at dispatch so
-- the callback surface can substitute it (gateway.go § deliverTo override).
ALTER TABLE turn_context ADD COLUMN return_to TEXT NOT NULL DEFAULT '';
