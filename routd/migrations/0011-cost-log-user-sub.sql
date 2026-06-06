-- Per-user cost cap (spec 5/34). gated's budgetGate binds the LOWER of the
-- folder cap (groups.cost_cap_cents_per_day) and a per-user cap keyed on the
-- JWT-derived caller sub. routd's cost_log dropped user_sub at the federation
-- split, so the per-user branch couldn't fire. Restore it here so routd's
-- budgetGate matches gated:
--
--   * cost_log gains user_sub (mirrors store/0049-cost-log.sql) so per-turn
--     rows carry the caller, and SpendTodayUser can aggregate them.
--   * auth_users mirrors the FINAL gated shape (store 0001 + 0040 linked_to_sub
--     + 0049 cost_cap_cents_per_day) so store.UserCap / SetUserCap / CreateAuthUser
--     read+write routd.db verbatim — the SAME source gated reads (a sub-keyed cap
--     column), now owned in routd.db like acl/secrets/tasks/pane.
ALTER TABLE cost_log ADD COLUMN user_sub TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_cost_log_user_day ON cost_log(user_sub, recorded_at);

CREATE TABLE auth_users (
  id          INTEGER PRIMARY KEY,
  sub         TEXT UNIQUE NOT NULL,
  username    TEXT UNIQUE NOT NULL,
  hash        TEXT NOT NULL,
  name        TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  linked_to_sub TEXT,
  cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_auth_users_linked_to_sub
  ON auth_users(linked_to_sub) WHERE linked_to_sub IS NOT NULL;
