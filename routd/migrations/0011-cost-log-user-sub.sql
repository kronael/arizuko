-- Per-user cost cap (spec 5/34). budgetGate binds the LOWER of the folder cap
-- and a per-user cap keyed on the JWT-derived caller sub, so a per-turn cost
-- row carries the caller and SpendTodayUser can aggregate them.
--
-- The cap itself is a `user_profiles` attribute. That is an objstore entity
-- rather than a table this chain creates: the driver makes it, from the
-- declaration that sits beside the code writing it (store.EnsureEntities, spec
-- 6/37 step 7), addressed by the sub. An entity keeps the name of the table it
-- replaces, so a database carrying rows there is moved by `arizuko migrate-hot`
-- and refuses to open until it has been.
ALTER TABLE cost_log ADD COLUMN user_sub TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_cost_log_user_day ON cost_log(user_sub, recorded_at);
