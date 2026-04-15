-- Enforce one user per JID (prevent two users claiming the same JID)
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_jids_jid ON user_jids(jid);
