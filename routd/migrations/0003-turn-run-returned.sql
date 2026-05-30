-- The run-live marker (spec 5/E § Post-terminal callbacks). turn_context
-- splits two terminal signals: state='done' (set by submit_turn or the
-- run-response) guards a double LIVE run, while run_returned=1 (set only
-- when POST /v1/runs returns) gates the callback 409. An early submit_turn
-- flips state→done but leaves run_returned=0, so trailing reply/send
-- callbacks the still-live run emits stay valid until the run returns.
ALTER TABLE turn_context ADD COLUMN run_returned INTEGER NOT NULL DEFAULT 0;
