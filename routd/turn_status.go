package routd

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
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

type turnEnd int

const (
	endDone turnEnd = iota
	endFailed
	endSteered
	endRetired
)

func (e turnEnd) mark() string {
	if e == endFailed {
		return failedMark
	}
	return doneMark
}

func canShowProgress(dl Deliverer, jid string) bool {
	return dl != nil && dl.HasCapability(jid, "edit") && dl.HasCapability(jid, "delete")
}

func (l *Loop) beginProgress(jid string) {
	if l.deliver == nil {
		return
	}
	// Presence failure must not fail a turn.
	_ = l.deliver.Typing(jid, true)
}

func (l *Loop) noteParked(chatJID string) {
	if l.deliver == nil {
		return
	}
	if p, err := l.db.store().ReadModelPause(context.Background(), l.folderForJid(chatJID)); err != nil || p != nil {
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

func (s *Server) updateProgress(tc TurnContext, jid, text string) error {
	if !store.IsActiveTurnState(tc.State) {
		return nil
	}
	return applyStatus(s.db, s.deliver, tc, jid, turnStatus{phase: "progress", text: strings.TrimSpace(text)})
}

func finishTurn(db *DB, dl Deliverer, turnID string, end turnEnd) {
	unlock := lockTurn(turnID)
	defer unlock()
	finishTurnLocked(db, dl, turnID, end)
}

func finishTurnLocked(db *DB, dl Deliverer, turnID string, end turnEnd) {
	db.progress.Delete(turnID)
	if end == endSteered || end == endRetired {
		if err := db.SetTurnState(turnID, store.TurnDone); err != nil {
			slog.Error("finish turn without status", "turn_id", turnID, "err", err)
		}
		return
	}
	tc, ok := db.GetTurnContext(turnID)
	if !ok {
		return
	}
	committed, err := db.store().FinishTurnStatus(context.Background(), tc)
	if err != nil || !committed {
		slog.Error("persist turn completion", "turn_id", turnID, "err", err)
		return
	}
	if tc.StatusOwed == "final" || tc.StatusOwed == "stopped" {
		return
	}
	tc.State, tc.StatusOwed = store.TurnDone, "cleared"
	if err := endProgress(db, dl, turnID, end); err != nil {
		slog.Error("queue status cleanup", "turn_id", turnID, "err", err)
		return
	}
	if err := db.store().AcknowledgeTurnStatus(context.Background(), tc); err != nil {
		slog.Error("acknowledge status cleanup", "turn_id", turnID, "err", err)
	}
}

func (l *Loop) retryProgress(now time.Time, pending store.PendingMessage) bool {
	if pending.Verb != verbStatus {
		return false
	}
	unlock := lockTurn(pending.TurnID)
	defer unlock()
	current, ok, err := l.db.store().PendingDelivery(context.Background(), pending.Message)
	if err != nil {
		slog.Error("read status delivery", "err", err)
		return true
	}
	if !ok {
		return true
	}
	if current.StatusRevision == 0 {
		active, err := l.db.store().ActiveProgress(current.ChatJID, current.Topic, current.TurnID)
		if err != nil {
			slog.Error("read active status", "err", err)
			return true
		}
		if len(active) == 0 {
			if err := l.db.DeadLetter(current.Message, 0); err != nil {
				slog.Error("retire status", "err", err)
			}
			return true
		}
		l.retryOutbound(now, current)
		return true
	}
	if now.Sub(current.QueuedAt) < backoffDelay(current.Attempts) {
		return true
	}
	if err := executeStatus(l.db, l.deliver, current, l.maxOutboundRetry); err != nil {
		slog.Warn("retry status delivery", "err", err)
	}
	return true
}

func endProgress(db *DB, dl Deliverer, turnID string, end turnEnd) error {
	tc, ok := db.GetTurnContext(turnID)
	if !ok {
		return fmt.Errorf("no turn context for %s", turnID)
	}
	jids, err := statusDestinations(db, tc)
	if err != nil {
		return err
	}
	if dl != nil {
		clearPresence(dl, jids)
	}
	for _, jid := range jids {
		row, ok := db.LastStatusRow(jid, turnID)
		if !ok {
			continue
		}
		body := strings.TrimPrefix(row.Content, progressMark)
		if err := applyStatus(db, dl, tc, jid, turnStatus{phase: "cleared", text: body, end: end}); err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) pauseProgress(jid, runID string) {
	if l.deliver == nil {
		return
	}
	clearPresence(l.deliver, l.db.chatsOwnedBy(jid, runID))
}

func clearPresence(dl Deliverer, jids []string) {
	for _, jid := range jids {
		// Presence failure must not fail a turn.
		_ = dl.Typing(jid, false)
	}
}

type turnStatus struct {
	end        turnEnd
	scope      string
	spent, cap int
	phase      string
	cause      string
	retry, max int
	at         time.Time
	text       string
}

func renderTurnStatus(status turnStatus) string {
	switch status.phase {
	case "cleared":
		return status.end.mark() + status.text
	case "budget":
		return fmt.Sprintf("Budget reached for today (%s spent %d of %d cents). Resumes at 00:00 UTC.", status.scope, status.spent, status.cap)
	case "progress":
		return progressMark + status.text
	case "waiting":
		return fmt.Sprintf("%s. Retry %d/%d at %s UTC.", status.cause, status.retry, status.max, status.at.UTC().Format("15:04:05"))
	case "running":
		return fmt.Sprintf("Retry %d/%d is running.", status.retry, status.max)
	case "final":
		if status.retry > 0 {
			word := "retries"
			if status.retry == 1 {
				word = "retry"
			}
			return fmt.Sprintf("%s. Stopped after %d %s.", status.cause, status.retry, word)
		}
		return status.cause + "."
	case "recovered":
		return "The request completed."
	case "stopped":
		return "Turn stopped."
	case "timeout":
		return "Time limit reached. Say 'continue' to resume, or narrow the request."
	case "silent":
		return "Agent could not produce a reply. Please re-send."
	case "setup":
		return "Agent setup needs attention. Ask an operator to check the group."
	default:
		return status.text
	}
}

func applyStatus(db *DB, dl Deliverer, tc TurnContext, jid string, status turnStatus) error {
	place, err := placeTurnDelivery(db, tc, jid, "", true)
	if err != nil {
		return err
	}
	row, exists := db.LastStatusRow(place.jid, tc.TurnID)
	if exists && row.StatusPhase == "final" {
		return nil
	}
	if status.phase == "progress" && status.text == "" {
		return nil
	}
	if status.phase == "cleared" && (!exists || row.StatusPhase == "cleared") {
		return nil
	}
	if !exists {
		row = core.Message{ID: fmt.Sprintf("status-%x", sha256.Sum256([]byte(tc.TurnID+"\x00"+place.jid))), ChatJID: place.jid, Timestamp: time.Now(), Sender: tc.Folder, FromMe: true, BotMsg: true, Verb: verbStatus, TurnID: tc.TurnID, RoutedTo: tc.Folder, Topic: place.topic()}
	}
	if status.phase == "cleared" && status.text == "" {
		status.text = strings.TrimPrefix(row.Content, progressMark)
	}
	if jidScheme(place.jid) == "email" && exists {
		if status.phase == "waiting" || status.phase == "running" || status.phase == "progress" {
			return nil
		}
		if status.phase == "cleared" {
			status = turnStatus{phase: "recovered"}
		}
		if row.PlatformID == "" {
			if err := db.store().QueueStatus(context.Background(), row, renderTurnStatus(turnStatus{phase: "cleared", text: row.Content}), "cleared"); err != nil {
				return err
			}
			if err := flushStatus(db, dl, row); err != nil {
				return err
			}
		}
		row.ID = "final-" + row.ID
		row.Timestamp = time.Now()
		row.PlatformID = ""
		row.StatusRevision = 0
	}
	content := renderTurnStatus(status)
	phase := status.phase
	if phase == "timeout" || phase == "silent" || phase == "setup" || phase == "stopped" || phase == "recovered" || phase == "budget" {
		phase = "final"
	}
	if err := db.store().QueueStatus(context.Background(), row, content, phase); err != nil {
		return err
	}
	if exists && row.PlatformID == "" && row.StatusRevision > 0 && row.StatusPhase != "cleared" && status.phase != "cleared" {
		return nil
	}
	err = flushStatus(db, dl, row)
	if err != nil && (status.phase != "progress" || row.PlatformID == "") {
		slog.Warn("status operation queued", "turn_id", tc.TurnID, "jid", place.jid, "err", err)
		return nil
	}
	return err
}

func flushStatus(db *DB, dl Deliverer, row core.Message) error {
	if dl == nil {
		return nil
	}
	pending, ok, err := db.store().PendingDelivery(context.Background(), row)
	if err != nil || !ok {
		return err
	}
	return executeStatus(db, dl, pending, defaultMaxOutboundRetry)
}

func executeStatus(db *DB, dl Deliverer, pending store.PendingMessage, maxAttempts int) error {
	row := pending.Message
	pid := ""
	var err error
	switch pending.Operation {
	case "edit":
		err = dl.Edit(row.ChatJID, row.PlatformID, row.Content, row.Topic)
	case "delete":
		if row.PlatformID != "" {
			err = dl.Delete(row.ChatJID, row.PlatformID, row.Topic)
		}
	default:
		place := deliveryPlace{jid: row.ChatJID, threadID: row.Topic}
		if tc, ok := db.GetTurnContext(row.TurnID); ok {
			place, err = placeTurnDelivery(db, tc, row.ChatJID, row.Topic, true)
		}
		if err == nil {
			key := row.StatusSendKey
			if key == "" {
				key = row.ID
			}
			pid, err = dl.Send(place.jid, row.Content, row.ReplyToID, place.threadID, place.threadRoot, key)
		}
	}
	if err == nil {
		return db.MarkDelivered(row, pid)
	}
	permanent := errors.Is(err, chanlib.ErrPermanent) || errors.Is(err, chanlib.ErrUnsupported) || errors.Is(err, chanlib.ErrInvalidRequest)
	if saveErr := db.store().FailStatus(context.Background(), pending, permanent, maxAttempts); saveErr != nil {
		return saveErr
	}
	return err
}

func (l *Loop) publishStatus(tc TurnContext, status turnStatus) error {
	unlock := lockTurn(tc.TurnID)
	defer unlock()
	return l.publishStatusLocked(tc, status)
}

func (l *Loop) publishStatusLocked(tc TurnContext, status turnStatus) error {
	l.db.progress.Delete(tc.TurnID)
	jids, err := statusDestinations(l.db, tc)
	if err != nil {
		return err
	}
	for _, jid := range jids {
		if err := applyStatus(l.db, l.deliver, tc, jid, status); err != nil {
			return err
		}
	}
	return nil
}

func turnFailureCause(out runedv1.RunOutcome, err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "Time limit reached"
	}
	if errors.Is(err, syscall.ENOSPC) {
		return "Server storage is full"
	}
	text := strings.ToLower(out.Error)
	switch {
	case strings.HasPrefix(text, "guest timed out"):
		return "Time limit reached"
	case strings.HasPrefix(text, "guest exited"), strings.HasPrefix(text, "guest ended without an exit frame"):
		return "Agent stopped unexpectedly"
	case strings.HasPrefix(text, "group ") && strings.Contains(text, "invalid group agent:"), strings.HasPrefix(text, "render"):
		return "Agent setup needs attention"
	case strings.HasPrefix(text, "virtiofsd "), strings.HasPrefix(text, "vm record:"), strings.HasPrefix(text, "qemu start:"), strings.HasPrefix(text, "prepare "):
		if strings.Contains(text, "no space left on device") {
			return "Server storage is full"
		}
		return "Agent could not start"
	default:
		return "Agent could not complete the request"
	}
}

func (l *Loop) publishOwedStatus(tc TurnContext) error {
	if tc.StatusOwed == "" {
		return nil
	}
	unlock := lockTurn(tc.TurnID)
	defer unlock()
	return l.publishOwedStatusLocked(tc)
}

func (l *Loop) publishOwedStatusLocked(tc TurnContext) error {
	current, ok := l.db.GetTurnContext(tc.TurnID)
	if !ok || current.AttemptID != tc.AttemptID || current.StatusOwed == "" {
		return nil
	}
	status := turnStatus{phase: current.StatusOwed, cause: current.FailureCause, retry: current.RetryCount, max: l.maxTurnRetry, at: current.NextAttemptAt}
	if current.State == store.TurnDone && (status.phase == "waiting" || status.phase == "running") {
		status.phase = "cleared"
	}
	if status.cause == "" {
		status.cause = "Agent could not complete the request"
	}
	if current.State == store.TurnRunning && current.ProviderFailureAttempt == current.AttemptID {
		control, _, err := l.db.store().ReadTurnControl(context.Background())
		if err != nil {
			return err
		}
		status.at = control.NextProbeAt
		status.retry++
		if current.RetryCount >= l.maxTurnRetry {
			status.phase = "final"
			status.retry = current.RetryCount
		}
	}
	if err := l.publishStatusLocked(current, status); err != nil {
		return err
	}
	return l.db.store().AcknowledgeTurnStatus(context.Background(), current)
}

func (l *Loop) recoverStatuses() {
	turns, err := l.db.store().OwedTurnStatuses(context.Background())
	if err != nil {
		slog.Error("read owed turn statuses", "err", err)
		return
	}
	for _, tc := range turns {
		if err := l.publishOwedStatus(tc); err != nil {
			slog.Warn("publish owed turn status", "turn_id", tc.TurnID, "err", err)
		}
	}
}

func statusDestinations(db *DB, tc TurnContext) ([]string, error) {
	jids := db.chatsOwnedBy(returnTarget(tc, tc.ChatJID), tc.RunID)
	if tc.RetryCount > 0 {
		prior, err := db.store().TurnStatusChats(context.Background(), tc.TurnID)
		if err != nil {
			return nil, err
		}
		for _, jid := range prior {
			if !slices.Contains(jids, jid) {
				jids = append(jids, jid)
			}
		}
	}
	return jids, nil
}

func hasStoppedStatus(db *DB, jid string, turnIDs []string) bool {
	for _, id := range turnIDs {
		row, ok := db.LastStatusRow(jid, id)
		if ok && row.StatusPhase == "final" && row.Content == renderTurnStatus(turnStatus{phase: "stopped"}) {
			return true
		}
	}
	return false
}
