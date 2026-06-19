package store

import (
	"database/sql"
	"time"
)

// PaneSession is a Slack assistant pane session — one per (team, user,
// thread_ts) tuple. channel_id is the DM channel where the pane lives;
// context_jid is the workspace channel the user is viewing while the
// pane is open (may be empty).
type PaneSession struct {
	TeamID       string
	UserID       string
	ThreadTS     string
	ChannelID    string
	ContextJID string
	OpenedAt   string
}

// UpsertPane inserts or updates the pane row for (team, user, thread_ts).
// Sets opened_at on insert only; channel_id is refreshed on every call.
func (s *Store) UpsertPane(teamID, userID, threadTS, channelID string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(
		`INSERT INTO pane_sessions (team_id, user_id, thread_ts, channel_id, opened_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(team_id, user_id, thread_ts) DO UPDATE SET
		   channel_id = excluded.channel_id`,
		teamID, userID, threadTS, channelID, now,
	)
	return err
}

// GetPaneByChannel returns the pane keyed by its DM channel_id. Slack
// pane DMs are 1:1 between user and app, so channel_id uniquely keys
// the pane (within a team) even though the primary key is the triple.
// Returns (zero, false) when no row exists.
func (s *Store) GetPaneByChannel(channelID string) (PaneSession, bool) {
	var p PaneSession
	var ctx sql.NullString
	err := s.db.QueryRow(
		`SELECT team_id, user_id, thread_ts, channel_id, context_jid, opened_at
		 FROM pane_sessions WHERE channel_id = ?
		 ORDER BY opened_at DESC LIMIT 1`,
		channelID,
	).Scan(&p.TeamID, &p.UserID, &p.ThreadTS, &p.ChannelID, &ctx, &p.OpenedAt)
	if err != nil {
		return PaneSession{}, false
	}
	p.ContextJID = ctx.String
	return p, true
}

// SetPaneContext updates the workspace-channel context the user is
// viewing while the pane is open. Empty contextJID clears it.
func (s *Store) SetPaneContext(teamID, userID, threadTS, contextJID string) error {
	var v any
	if contextJID == "" {
		v = nil
	} else {
		v = contextJID
	}
	_, err := s.db.Exec(
		`UPDATE pane_sessions SET context_jid = ?
		 WHERE team_id = ? AND user_id = ? AND thread_ts = ?`,
		v, teamID, userID, threadTS,
	)
	return err
}

// SetPaneContextByChannel updates the workspace-channel context for the pane
// keyed by its DM channel_id (the triple-PK twin of SetPaneContext, but reached
// by channel_id the way GetPaneByChannel reads). Empty contextJID clears it.
// Backs routd's POST /v1/pane in the split topology, where slakd hands routd a
// {channel_id, jid} instead of opening pane_sessions itself. No-op when no pane
// row matches channel_id (slakd opens the pane via UpsertPane first).
func (s *Store) SetPaneContextByChannel(channelID, contextJID string) error {
	var v any
	if contextJID == "" {
		v = nil
	} else {
		v = contextJID
	}
	_, err := s.db.Exec(
		`UPDATE pane_sessions SET context_jid = ? WHERE channel_id = ?`,
		v, channelID,
	)
	return err
}
