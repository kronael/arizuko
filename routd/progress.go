package routd

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

const (
	verbStatus       = "status"
	progressMark     = "⏳ "
	doneMark         = "✓ "
	failedMark       = "✗ "
	progressLimit    = 1999 - len(progressMark)
	progressInterval = 5 * time.Second
)

func safeProgressText(text string) string {
	return strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cf, r) {
			return -1
		}
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return ' '
		}
		switch r {
		case '@':
			return '＠'
		case '*':
			return '∗'
		case '_':
			return '＿'
		case '`':
			return '｀'
		case '~':
			return '～'
		case '[':
			return '［'
		case ']':
			return '］'
		case '(':
			return '（'
		case ')':
			return '）'
		case '<':
			return '＜'
		case '>':
			return '＞'
		case '#':
			return '＃'
		case '\\':
			return '＼'
		}
		return r
	}, text)
}

func cutProgress(text string, budget int) string {
	if len(text) <= budget {
		return text
	}
	if budget <= 0 {
		return ""
	}
	for budget > 0 && !utf8.RuneStart(text[budget]) {
		budget--
	}
	return text[:budget]
}

func boundProgress(text string) string { return cutProgress(text, progressLimit) }

func renderChecklist(progress *turnProgress) string {
	var lines []string
	current := -1
	for _, source := range []string{"claude", "codex"} {
		for _, item := range progress.sources[source] {
			mark, label := "☐", item.Content
			if item.Status == "completed" {
				mark = "☑"
			}
			if item.Status == "in_progress" {
				mark = "⏳"
				if current < 0 {
					current = len(lines)
				}
				if item.ActiveForm != nil && *item.ActiveForm != "" {
					label = *item.ActiveForm
				}
			}
			lines = append(lines, mark+" "+safeProgressText(label))
		}
	}
	if len(lines) == 0 {
		return "No remaining tasks."
	}
	joined := strings.Join(lines, "\n")
	if len(joined) <= progressLimit {
		return joined
	}
	if current < 0 {
		current = 0
	}
	reserve := len(fmt.Sprintf("\n… %d tasks omitted", len(lines)-1))
	selected := make([]string, len(lines))
	selected[current] = cutProgress(lines[current], progressLimit-reserve)
	used := len(selected[current]) + reserve
	count := 1
	for i, line := range lines {
		if i != current && used+1+len(line) <= progressLimit {
			selected[i] = line
			used += 1 + len(line)
			count++
		}
	}
	var kept []string
	for _, line := range selected {
		if line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n") + fmt.Sprintf("\n… %d tasks omitted", len(lines)-count)
}

func (s *Server) publishProgress(tc TurnContext, progress *turnProgress, now time.Time, immediate bool) error {
	if !tc.Addressed {
		return nil
	}
	jid := returnTarget(tc, tc.ChatJID)
	if s.mutedGroup(tc.Folder) || !canShowProgress(s.deliver, jid) {
		slog.Debug("progress update dropped: no line can live here", "turn_id", tc.TurnID, "jid", jid)
		return nil
	}
	row, exists := s.db.LastStatusRow(jid, tc.TurnID)
	if exists && row.PlatformID != "" && row.Content == progressMark+progress.pendingText {
		if progress.lastPublished.IsZero() {
			progress.lastPublished = now
		}
		progress.pendingText = ""
		return nil
	}
	if exists && row.PlatformID == "" {
		return s.updateProgress(tc, jid, progress.pendingText)
	}
	if !immediate && !progress.lastPublished.IsZero() && now.Sub(progress.lastPublished) < progressInterval {
		return nil
	}
	if !immediate && !progress.lastAttempt.IsZero() && now.Sub(progress.lastAttempt) < progressInterval {
		return nil
	}
	progress.lastAttempt = now
	if err := s.updateProgress(tc, jid, progress.pendingText); err != nil {
		if errors.Is(err, chanlib.ErrPermanent) || errors.Is(err, chanlib.ErrInvalidRequest) || errors.Is(err, chanlib.ErrUnsupported) {
			progress.pendingText = ""
		}
		return err
	}
	row, exists = s.db.LastStatusRow(jid, tc.TurnID)
	if exists && row.PlatformID != "" && row.Content == progressMark+progress.pendingText {
		progress.lastPublished = now
		progress.pendingText = ""
	}
	return nil
}

func (s *Server) flushProgress(now time.Time) {
	if s == nil || s.db == nil {
		return
	}
	s.db.progress.Range(func(key, value any) bool {
		turnID := key.(string)
		unlock := lockTurn(turnID)
		defer unlock()
		current, ok := s.db.progress.Load(turnID)
		if !ok || current != value {
			return true
		}
		progress := value.(*turnProgress)
		if progress.pendingText == "" {
			return true
		}
		tc, ok := s.db.GetTurnContext(turnID)
		if !ok || !store.IsActiveTurnState(tc.State) || s.callbackClosed(tc) {
			return true
		}
		if err := s.publishProgress(tc, progress, now, false); err != nil {
			slog.Warn("progress flush failed", "turn_id", turnID, "err", err)
		}
		return true
	})
}

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

// canShowProgress is the one rule for where a progress line lives: a channel
// that edits it while the turn runs and deletes it when the turn ends.
func canShowProgress(dl Deliverer, jid string) bool {
	return dl != nil && dl.HasCapability(jid, "edit") && dl.HasCapability(jid, "delete")
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

// noteParked starts presence for a chat the queue parked behind another run,
// when its newest user row is addressed (the same test processGroupMessages
// applies). The run that later takes the chat ends it at End; a cursor advance
// with no run clears it (advance).
func (l *Loop) noteParked(chatJID string) {
	if l.deliver == nil {
		return
	}
	msgs, err := l.db.NewMessagesForChat(chatJID, l.db.GetAgentCursor(chatJID), pollBatch)
	if err != nil {
		return
	}
	i := newestUserIdx(msgs)
	if i < 0 {
		return
	}
	if row := l.resolveBatch(chatJID, msgs).rows[i]; !row.ok || !row.Addressed {
		return
	}
	reply := replyChat(chatJID, msgs[i])
	l.waiting.Store(chatJID, reply)
	l.beginProgress(reply)
}

// updateProgress is the only code that creates or edits a turn's verb=status
// row. The first update sends the message, a later one edits it and rewrites
// the row, and an update whose text the row already holds does nothing.
//
// A channel that cannot both edit and delete receives nothing. A hard edit failure
// returns to the caller unchanged and is never re-posted as a fresh message.
// The caller holds lockTurn(tc.TurnID).
func (s *Server) updateProgress(tc TurnContext, jid, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if !store.IsActiveTurnState(tc.State) {
		slog.Debug("progress update dropped: the turn already ended",
			"turn_id", tc.TurnID, "jid", jid)
		return nil
	}
	content := progressMark + text
	row, ok := s.db.LastStatusRow(jid, tc.TurnID)
	if !ok {
		row = s.outboundRow(tc, jid, content, "")
		row.Verb = verbStatus
		place, err := placeTurnDelivery(s.db, tc, jid, "", true)
		if err != nil {
			return err
		}
		row.ChatJID, row.Topic = place.jid, place.topic()
		if err := s.db.PutOutbound(row, core.MessageStatusPending); err != nil {
			return err
		}
		row, ok = s.db.store().MessageByID(row.ChatJID, row.ID)
		if !ok {
			return fmt.Errorf("progress row not found after append")
		}
		status := s.deliverRow(tc, place.jid, &row, place.threadRoot, place.threadID)
		switch status {
		case core.MessageStatusSent:
			return s.db.MarkDelivered(row, row.PlatformID)
		case core.MessageStatusFailed:
			return s.db.DeadLetter(row, 0)
		default:
			return nil
		}
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
			return err
		}
		if err != nil {
			return err
		}
	}
	return db.SetMessageContent(row, content)
}

// endStatusRow removes the progress line and rewrites the row to its terminal
// form without a platform edit. A line whose first send never landed has
// nothing to delete; it is dead-lettered so the outbound sweep never posts it.
// A refused delete returns and leaves the row as the user still sees it.
func endStatusRow(db *DB, dl Deliverer, jid string, row core.Message, terminal string) error {
	var err error
	if row.PlatformID == "" {
		err = db.DeadLetter(row, 0)
	} else {
		err = dl.Delete(jid, row.PlatformID, row.Topic)
	}
	if err != nil {
		return err
	}
	return db.SetMessageContent(row, terminal)
}

// finishTurn is the only code that marks a turn done. It ends the turn's
// progress first and flips the state last: a routd that dies between the two
// replays the turn, and the replay repeats the edit, which is idempotent.
//
// Ending the progress never fails the turn. The reply is the product and it is
// already delivered or owed; a line that was not deleted is logged at Error and
// the row keeps what the user sees.
func finishTurn(db *DB, dl Deliverer, turnID string, end turnEnd) {
	unlock := lockTurn(turnID)
	defer unlock()
	finishTurnLocked(db, dl, turnID, end)
}

func finishTurnLocked(db *DB, dl Deliverer, turnID string, end turnEnd) {
	db.progress.Delete(turnID)
	if end != endSteered && dl != nil {
		endProgress(db, dl, turnID, end)
	}
	if err := db.SetTurnState(turnID, store.TurnDone); err != nil {
		slog.Error("finishTurn: SetTurnState(done) failed; turn may appear stuck in-progress and be re-dispatched, duplicating output",
			"turn_id", turnID, "err", err)
	}
}

func (l *Loop) retryProgress(now time.Time, pending store.PendingMessage) bool {
	if pending.Verb != verbStatus {
		return false
	}
	unlock := lockTurn(pending.TurnID)
	defer unlock()
	row, ok := l.db.store().MessageByID(pending.ChatJID, pending.ID)
	if !ok || row.PlatformID != "" {
		return true
	}
	active, err := l.db.store().ActiveProgress(row.ChatJID, row.Topic, row.TurnID)
	if err != nil {
		slog.Error("retry progress: read active turn", "id", row.ID, "err", err)
		return true
	}
	if len(active) == 0 {
		if err := l.db.DeadLetter(row, 0); err != nil {
			slog.Error("retry progress: dead-letter", "id", row.ID, "err", err)
		}
		return true
	}
	if _, ok := l.db.GetTurnContext(row.TurnID); !ok {
		return true
	}
	pending.Message = row
	l.retryOutbound(now, pending)
	return true
}

// endProgress clears presence on every chat the turn's run owns and deletes the
// turn's progress line, where one exists. A line already ended is left alone, so
// a second end of one turn (the result recorded, then the run returning) acts
// once.
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
		if err := endStatusRow(db, dl, jid, row, end.mark()+body); err != nil {
			slog.Error("turn ended but its progress line was not deleted",
				"folder", tc.Folder, "turn_id", turnID, "jid", jid, "err", err)
		}
	}
}

// pauseProgress clears presence for a run that returned without ending its
// turn: the batch re-feeds, a retry is scheduled, or runed was unreachable.
// The progress message stays ⏳, because the turn is not over.
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
