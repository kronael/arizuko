package routd

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/chanlib"
)

const (
	verbStatus   = "status"
	progressMark = "⏳ "
)

// beginProgress declares that a turn is running on the chat that receives its
// reply. Presence only: the progress message is created by the first update,
// so a turn that reports nothing posts nothing (specs/6/46-turn-progress.md).
func (l *Loop) beginProgress(jid string) {
	if l.deliver == nil {
		return
	}
	// Deliverer.Typing is best-effort by contract; a channel without presence
	// answers nil and a failed indicator must not fail the turn.
	_ = l.deliver.Typing(jid, true)
}

// updateProgress is the only code that creates or edits a turn's verb=status
// row. The first update sends the message, a later one edits it and rewrites
// the row, and an update whose text the row already holds does nothing: the
// agent's stream and its final result carry the same <status> block.
//
// A channel that does not advertise edit receives nothing. A hard edit failure
// returns to the caller unchanged and is never re-posted as a fresh message.
// The caller holds lockTurn(tc.TurnID).
func (s *Server) updateProgress(tc TurnContext, jid, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if s.deliver == nil || s.mutedGroup(tc.Folder) || !s.deliver.CanEdit(jid) {
		slog.Debug("progress update dropped: no editable message here",
			"turn_id", tc.TurnID, "jid", jid)
		return nil
	}
	content := progressMark + text
	row, ok := s.db.LastStatusRow(jid, tc.TurnID)
	if !ok {
		row = s.outboundRow(tc, jid, content, "")
		row.Verb = verbStatus
		status := s.deliverTurn(tc, jid, &row)
		return s.db.PutOutbound(row, status)
	}
	if row.Content == content {
		return nil
	}
	// A row with no platform id is still owed its first send: the outbound
	// sweep reads the row, so rewriting it is what edits that message.
	if row.PlatformID != "" {
		err := s.deliver.Edit(jid, row.PlatformID, content)
		if errors.Is(err, chanlib.ErrUnsupported) {
			slog.Warn("progress edit refused although the channel advertises edit",
				"turn_id", tc.TurnID, "jid", jid, "err", err)
			return nil
		}
		if err != nil {
			return err
		}
	}
	return s.db.SetMessageContent(row, content)
}
