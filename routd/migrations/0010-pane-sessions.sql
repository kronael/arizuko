-- routd owns the Slack agent pane sessions (spec 6/D, spec 5/5 § Daemon
-- ownership). routd is the central data plane (acl 0007, secrets 0008,
-- tasks 0009); it now OWNS the pane rows too, so paneHints reads routd.db's
-- own table instead of sibling-reading slakd's messages.db. This was the LAST
-- messages.db sibling-read in routd; after it, routd opens NO sibling DB.
-- Schema mirrors store/migrations/0056-pane-sessions.sql VERBATIM so
-- store.GetPaneByChannel/UpsertPane/SetPaneContext read it the same way.
-- slakd writes the pane via routd's POST /v1/pane (a separate slakd rewire).
CREATE TABLE pane_sessions (
  team_id        TEXT NOT NULL,
  user_id        TEXT NOT NULL,
  thread_ts      TEXT NOT NULL,
  channel_id     TEXT NOT NULL,
  context_jid    TEXT,
  opened_at      TEXT NOT NULL,
  last_status_at TEXT,
  PRIMARY KEY (team_id, user_id, thread_ts)
);
CREATE INDEX idx_pane_sessions_channel ON pane_sessions(channel_id);
