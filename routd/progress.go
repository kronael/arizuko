package routd

import (
	"errors"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

const (
	verbStatus   = "status"
	progressMark = "⏳ "
	doneMark     = "✓ "
	failedMark   = "✗ "
)

// turnEnd is how a turn ended, as its progress message must say it.
type turnEnd int

const (
	// endDone: a result was recorded, or the run exited clean.
	endDone turnEnd = iota
	// endFailed: retries or the run ended in error, or a clean run left neither
	// a result nor a reply.
	endFailed
	// endSteered: the turn's batch went into a live run, which reports under
	// its own turn. Nothing of this turn's is in progress to end.
	endSteered
)

func (e turnEnd) mark() string {
	if e == endFailed {
		return failedMark
	}
	return doneMark
}

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

func summaryID(turnID string) string { return "summary-" + turnID }

func readTaskSummary(db *DB, jid, turnID string) (core.Message, bool, error) {
	rows, err := db.MessagesSince(jid, "")
	if err != nil {
		return core.Message{}, false, err
	}
	for _, row := range rows {
		if row.ID == summaryID(turnID) {
			return row, true, nil
		}
	}
	return core.Message{}, false, nil
}

func (s *Server) declareSummary(tc TurnContext, jid, text string) error {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\r\n") {
		return errors.New("task summary must be one nonempty line")
	}
	if tc.State == store.TurnDone {
		return nil
	}
	if !summaryThreadEnabled(s.db, s.deliver, tc, jid) {
		return s.updateProgress(tc, jid, text)
	}
	_, ok, err := readTaskSummary(s.db, jid, tc.TurnID)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	row := s.outboundRow(tc, jid, "🤖 "+text, "")
	row.ID = summaryID(tc.TurnID)
	row.Verb = store.VerbStatusSummary
	status := s.deliverRow(tc, jid, &row, "", tc.Topic)
	return s.db.PutOutbound(row, status)
}

// updateProgress is the only code that creates or edits a turn's verb=status
// row. The first update sends the message, a later one edits it and rewrites
// the row, and an update whose text the row already holds does nothing.
//
// A channel that does not advertise edit receives nothing. A hard edit failure
// returns to the caller unchanged and is never re-posted as a fresh message.
// The caller holds lockTurn(tc.TurnID).
func (s *Server) updateProgress(tc TurnContext, jid, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if tc.State == store.TurnDone {
		slog.Debug("progress update dropped: the turn already ended",
			"turn_id", tc.TurnID, "jid", jid)
		return nil
	}
	content := progressMark + text
	row, ok := s.db.LastStatusRow(jid, tc.TurnID)
	if !ok {
		row = s.outboundRow(tc, jid, content, "")
		row.Verb = verbStatus
		place, err := placeTurnDelivery(s.db, s.deliver, tc, jid, "", true)
		if err != nil {
			return err
		}
		status := s.deliverRow(tc, place.jid, &row, place.threadRoot, place.threadID)
		return s.db.PutOutbound(row, status)
	}
	if row.Content == content {
		return nil
	}
	return rewriteStatusRow(s.db, s.deliver, tc.TurnID, jid, row, content)
}

// rewriteStatusRow puts content on a status row and on the message it names. A
// row with no platform id is still owed its first send: the outbound sweep
// reads the row, so rewriting it is what edits that message. A refused edit
// leaves the row as the user still sees it.
func rewriteStatusRow(db *DB, dl Deliverer, turnID, jid string, row core.Message, content string) error {
	if row.PlatformID != "" {
		err := dl.Edit(jid, row.PlatformID, content, row.Topic)
		if errors.Is(err, chanlib.ErrUnsupported) {
			slog.Warn("progress edit refused although the channel advertises edit",
				"turn_id", turnID, "jid", jid, "err", err)
			return nil
		}
		if err != nil {
			return err
		}
	}
	return db.SetMessageContent(row, content)
}

// finishTurn is the only code that marks a turn done. It ends the turn's
// progress first and flips the state last: a routd that dies between the two
// replays the turn, and the replay repeats the edit, which is idempotent.
//
// Ending the progress never fails the turn. The reply is the product and it is
// already delivered or owed; a message left reading ⏳ is logged at Error and
// the row keeps what the user sees.
func finishTurn(db *DB, dl Deliverer, turnID string, end turnEnd) {
	unlock := lockTurn(turnID)
	defer unlock()
	finishTurnLocked(db, dl, turnID, end)
}

func finishTurnLocked(db *DB, dl Deliverer, turnID string, end turnEnd) {
	if end != endSteered && dl != nil {
		endProgress(db, dl, turnID, end)
	}
	if err := db.SetTurnState(turnID, store.TurnDone); err != nil {
		slog.Error("finishTurn: SetTurnState(done) failed; turn may appear stuck in-progress and be re-dispatched, duplicating output",
			"turn_id", turnID, "err", err)
	}
}

// endProgress clears presence on every chat the turn's run owns and turns the
// turn's progress message, where one exists, from ⏳ into its terminal form.
// A message already terminal is left alone, so a second end of one turn (the
// result recorded, then the run returning) edits once.
func endProgress(db *DB, dl Deliverer, turnID string, end turnEnd) {
	tc, ok := db.GetTurnContext(turnID)
	if !ok {
		slog.Warn("endProgress: no turn context; progress not ended", "turn_id", turnID)
		return
	}
	jids := db.chatsOwnedBy(returnTarget(tc, tc.ChatJID), tc.RunID)
	clearPresence(dl, jids)
	for _, jid := range jids {
		row, ok := db.LastStatusRow(jid, turnID)
		if !ok {
			continue
		}
		body, pending := strings.CutPrefix(row.Content, progressMark)
		if !pending {
			continue
		}
		if err := rewriteStatusRow(db, dl, turnID, jid, row, end.mark()+body); err != nil {
			slog.Error("turn ended but its progress message still reads in-progress",
				"folder", tc.Folder, "turn_id", turnID, "jid", jid, "err", err)
		}
	}
}

// pauseProgress clears presence for a run that returned without ending its
// turn: the batch re-feeds, a retry is scheduled, or runed was busy or
// unreachable. The progress message stays ⏳, because the turn is not over.
func (l *Loop) pauseProgress(jid, runID string) {
	if l.deliver == nil {
		return
	}
	clearPresence(l.deliver, l.db.chatsOwnedBy(jid, runID))
}

// clearPresence is best-effort by contract, as in beginProgress.
func clearPresence(dl Deliverer, jids []string) {
	for _, jid := range jids {
		_ = dl.Typing(jid, false)
	}
}
