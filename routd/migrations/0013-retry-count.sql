-- Turn retry: reschedule on SIGKILL/OOM/timeout. retry_count tracks attempts;
-- a turn without a reply increments and requeues until MAX_TURN_RETRY.
ALTER TABLE turn_context ADD COLUMN retry_count INTEGER NOT NULL DEFAULT 0;
