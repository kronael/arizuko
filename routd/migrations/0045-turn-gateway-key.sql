-- The turn records which key metered it (spec 6/39).
--
-- LiteLLM names a key in its spend log by sha256(key), never by the key, so
-- this is the pointer that resolves a turn to the gateway's own numbers. It is
-- the turn's identity, not its cost: arizuko keeps no copy of what was spent,
-- and GET /v1/cost?turn_id= answers by asking the gateway with this hash.
--
-- Empty for a turn that ran with no gateway configured, which is the state that
-- has no meter and therefore no per-turn number to report.
ALTER TABLE turn_context ADD COLUMN gateway_key_hash TEXT NOT NULL DEFAULT '';
