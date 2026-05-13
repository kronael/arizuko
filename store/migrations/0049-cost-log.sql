-- Spec 5/34 cost-caps. One row per LLM call (Anthropic + oracle subcall);
-- gateway aggregates daily for budget enforcement.
CREATE TABLE IF NOT EXISTS cost_log (
  ts          TEXT NOT NULL,
  folder      TEXT NOT NULL DEFAULT '',
  user_sub    TEXT NOT NULL DEFAULT '',          -- '' = channel-scoped turn
  model       TEXT NOT NULL,                     -- e.g. claude-opus-4-7, gpt-5, codex-mini
  input_tok   INTEGER NOT NULL DEFAULT 0,
  cache_read  INTEGER NOT NULL DEFAULT 0,        -- cheaper input tokens
  cache_write INTEGER NOT NULL DEFAULT 0,        -- cache-creation tokens
  output_tok  INTEGER NOT NULL DEFAULT 0,
  cents       INTEGER NOT NULL DEFAULT 0         -- precomputed via prices table
);
CREATE INDEX IF NOT EXISTS idx_cost_log_folder_ts ON cost_log(folder, ts);
CREATE INDEX IF NOT EXISTS idx_cost_log_user_ts ON cost_log(user_sub, ts);

-- Per-folder cap lives on groups (folder-keyed); per-user cap on auth_users (sub-keyed).
-- The 5/34 spec mentioned chats; chats is JID-keyed today (post-0023) so folder-level
-- config belongs on groups.
ALTER TABLE groups     ADD COLUMN cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0;
ALTER TABLE auth_users ADD COLUMN cost_cap_cents_per_day INTEGER NOT NULL DEFAULT 0;
