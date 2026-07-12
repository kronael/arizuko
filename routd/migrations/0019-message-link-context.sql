-- link-context carry (spec 5/W § link context): a message that arrived through
-- a context-bearing route token snapshots the token's context at ingest, so
-- prompt-build reads the trigger row — no ambiguous chat_jid→token lookup when
-- a folder has several active tokens on one JID. NULL = not token-borne or a
-- context-less token; behavior unchanged.
ALTER TABLE messages ADD COLUMN link_context TEXT;
