-- specs/6/D: Slack agent pane (assistant.threads.*) sessions.
-- One row per (team_id, user_id, thread_ts) — Slack's pane identity.
-- channel_id is the DM channel where the pane lives (lookups by it
-- map outbound back to a pane). context_jid is the workspace channel
-- the user is viewing while pane is open (assistant_thread_context_changed).
-- Timestamps are RFC3339Nano UTC computed by Go; no strftime.

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
