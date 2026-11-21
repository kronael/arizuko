package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/retry"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/types"
)

func (l *Loop) processGroupMessages(chatJID string) (bool, error) {
	l.q.mu.Lock()
	s := l.q.getGroup(chatJID)
	s.refused, s.candidate = false, true
	l.q.mu.Unlock()
	defer func() {
		l.q.mu.Lock()
		s.candidate = false
		l.q.mu.Unlock()
	}()
	if turnID, ok := strings.CutPrefix(chatJID, deferredWorkPrefix); ok {
		return l.replay(turnID)
	}
	return l.processChat(chatJID, false)
}

func (l *Loop) processChat(chatJID string, commandsOnly bool) (bool, error) {
	unlock := sync.OnceFunc(lockTurn("\x00chat:" + chatJID))
	defer unlock()
	msgs, b, err := l.pendingBatch(chatJID, l.db.GetAgentCursor(chatJID), pollBatch)
	if err != nil {
		return false, err
	}
	if len(msgs) == 0 {
		return false, nil
	}
	last := msgs[len(msgs)-1]

	// Three questions, three rows. The busy key is the newest user row; the steer
	// offer takes the oldest slash command; the folder comes from the newest row
	// that dispatches on its own (resolveBatch). Sharing one row made a `/root`
	// followed by an @mention miss a mention-only route and drop the batch
	// (BUGS L56, L336), and a newer review row that matched only ?observe took a
	// pending mention with it (BUGS L551).
	busyIdx := newestUserIdx(msgs)
	if busyIdx < 0 {

		// An all-bot batch is normally an agent's own output. When another folder
		// wrote it, it is a delegation that woke nothing (BUGS L2), so say so. The
		// chat's folder is READ from the mint record, never parsed from the JID —
		// the grammar is ambiguous (BUGS L4, L1).
		if author := l.directFolder(chatJID); author != "" && last.Sender != "" && last.Sender != author {
			slog.Warn("delegation woke nothing: the chat holds only bot rows, so no turn ran",
				"chat_jid", chatJID, "wrote_by", last.Sender, "chat_folder", author,
				"rows", len(msgs))
		}
		l.advance(chatJID, last)
		return false, nil
	}
	if !b.ok {
		l.routeMiss(chatJID, msgs, b.resolution)
		return false, nil
	}
	r := b.resolution
	folder := r.Folder

	wakeCursor := store.MessageCursor(msgs[busyIdx])
	steerConsumed := false
	steerIdx := steerRowIdx(msgs)
	steerMsg := msgs[steerIdx]
	if commandsOnly && !l.canSteer(chatJID, steerMsg, folder, b.rows[steerIdx].AtName) {
		return false, nil
	}
	owned := map[string]bool{}
	if commandsOnly {
		l.q.mu.Lock()
		turnID := l.q.reservations[chatJID].turnID
		l.q.mu.Unlock()
		tc, ok := l.db.GetTurnContext(turnID)
		if ok && !tc.RunReturned {
			for _, key := range tc.InputKeys {
				owned[key.Sort] = true
			}
		}
	}
	isOwned := func(msg core.Message) bool {
		return owned[store.MessageCursor(msg)]
	}
	var deferredSteer *core.Message
	if steerIdx > 0 && l.canSteer(chatJID, steerMsg, folder, b.rows[steerIdx].AtName) {
		for i, candidate := range msgs[:steerIdx] {
			if candidate.BotMsg || !b.rows[i].ok || isOwned(candidate) {
				continue
			}
			deferredSteer = &steerMsg
			msgs = msgs[:steerIdx]
			last = msgs[len(msgs)-1]
			break
		}
	}
	if deferredSteer == nil && l.steer(chatJID, steerMsg, folder, b.rows[steerIdx].AtName) {
		// Consumed HERE, exactly once, before anything else can fail: no steer
		// handler may run a second time because a later dispatch was refused.
		l.advance(chatJID, steerMsg)

		// A steer consumes its own row and no other: the older rows still get the
		// turn below (BUGS L104), and `last` stays at the steered row so the
		// closing advance does not walk the cursor back over the command.
		steerConsumed = true
		msgs, last = msgs[:steerIdx], steerMsg
	}
	if commandsOnly && deferredSteer == nil && !steerConsumed {
		return false, nil
	}
	waiting, err := l.db.store().LiveTurnsInChat(context.Background(), chatJID)
	if err != nil {
		return false, err
	}
	for _, tc := range waiting {
		if tc.State != store.TurnWaitingCapacity {
			continue
		}
		woke, err := l.db.store().WakeCapacityTurn(context.Background(), tc, wakeCursor, l.q.now())
		if err != nil {
			return false, err
		}
		if woke && l.isSavedExecution(tc) {
			l.Enqueue(deferredWorkPrefix + tc.TurnID)
		}
	}

	var trigger, ambient []core.Message
	addressed := map[string]bool{}
	pinned := map[string]bool{}
	for i, m := range msgs {
		switch {
		case m.BotMsg || isOwned(m):
		case b.rows[i].ok:
			if b.rows[i].AtName {
				m = l.takeAtName(chatJID, m, b.rows[i].Folder)
			}
			trigger = append(trigger, m)
			addressed[m.ID] = b.rows[i].Addressed
			pinned[m.ID] = b.rows[i].TopicPinned
		default:
			ambient = append(ambient, m)
		}
	}
	if len(ambient) > 0 {
		l.observeRows(chatJID, folder, ambient)
	}
	if len(trigger) == 0 {
		l.advance(chatJID, last)
		return steerConsumed, nil
	}

	web := strings.HasPrefix(chatJID, "web:")
	_, pin := l.db.StickyState(chatJID)
	turnTopic := func(m core.Message) string {
		if pinned[m.ID] {
			return m.Topic
		}
		if pin != "" {
			return pin
		}
		return m.Topic
	}

	// web: chats dispatch one turn per topic; everyone else per sender AND
	// topic. Keying on sender alone answered an older Discord thread inside a
	// newer one (BUGS L305).
	var groups [][]core.Message
	if web {
		groups = groupByTopic(trigger)
	} else {
		groups = consecutiveRuns(trigger, func(m core.Message) string {
			return m.Sender + "\x00" + turnTopic(m)
		})
	}

	var occurrences [][]core.Message
	for _, group := range groups {
		occurrences = append(occurrences, consecutiveRuns(group, func(m core.Message) string {
			if core.IsScheduledSender(m.Sender) {
				return m.ID
			}
			return ""
		})...)
	}
	groups = l.isolateElevated(occurrences)
	if deferredSteer != nil {
		for _, batch := range groups {
			last := batch[len(batch)-1]
			tc := TurnContext{TurnID: last.ID, ChatJID: chatJID, Folder: folder,
				Topic: turnTopic(last), Trigger: last.Sender, ReturnTo: last.ForwardedFrom,
				TriggerMsgID: last.ID, InputLast: store.MessageCursor(last),
				Addressed: slices.ContainsFunc(batch, func(m core.Message) bool { return addressed[m.ID] })}
			if err := l.q.savePending(l, tc, batch, ambient); err != nil {
				return false, err
			}
		}
		if l.steer(chatJID, *deferredSteer, folder, b.rows[steerIdx].AtName) {
			l.advance(chatJID, *deferredSteer)
			return true, nil
		}
		return false, nil
	}
	unlock()

	hadAny := false

	var marks []observedAdvance
	for gi, batch := range groups {
		bl := batch[len(batch)-1]
		topic := turnTopic(bl)

		owed := slices.ContainsFunc(batch, func(m core.Message) bool { return addressed[m.ID] })
		turnID := bl.ID
		for _, tc := range waiting {
			if tc.TurnID == turnID && tc.State == store.TurnWaitingCapacity {
				current, ok, err := l.db.store().ReadTurnContext(context.Background(), turnID)
				if err != nil {
					return hadAny, err
				}
				if ok && current.State == store.TurnWaitingCapacity && l.q.now().Before(current.NextAttemptAt) {
					return hadAny, nil
				}
			}
		}
		keys, err := turnMessageKeys(batch)
		if err != nil {
			return hadAny, err
		}
		for _, tc := range waiting {
			if tc.State != store.TurnWaitingCapacity || tc.TurnID == turnID {
				continue
			}
			current, ok, err := l.db.store().ReadTurnContext(context.Background(), tc.TurnID)
			if err != nil {
				return hadAny, err
			}
			if !ok || current.State != store.TurnWaitingCapacity || l.isSavedExecution(current) {
				continue
			}
			if slices.ContainsFunc(current.InputKeys, func(k objstore.Key) bool { return !slices.Contains(keys, k) }) {
				continue
			}
			retired, err := l.db.store().SupersedeCapacityTurn(context.Background(), current)
			if err != nil {
				return hadAny, err
			}
			if !retired {
				return hadAny, store.ErrRefused
			}
		}
		had, steered, busy, mark, derr := l.runBoundTurn(folder, topic, chatJID, turnID, owed, batch, ambient, nil, wakeCursor)
		if errors.Is(derr, errModelInputOrder) {
			if gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			return hadAny, l.holdModelInput(chatJID, topic, batch)
		}
		if errors.Is(derr, store.ErrModelPaused) {
			if gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			return hadAny, l.holdModelInput(chatJID, topic, batch)
		}
		if derr != nil {

			if !steerConsumed && gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			if steerConsumed {
				slog.Error("the rows a steer left behind did not reach runed; they are not re-fed",
					"jid", chatJID, "rows", len(batch), "ids", lostIDs(batch),
					"senders", lostSenders(batch), "err", derr)
			}
			return hadAny, derr
		}
		if busy {

			if !steerConsumed && gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			if steerConsumed {
				slog.Error("runed refused the rows a steer left behind; they are not re-fed",
					"jid", chatJID, "rows", len(batch), "ids", lostIDs(batch),
					"senders", lostSenders(batch))
			}
			return hadAny || steerConsumed, nil
		}
		if steered {

			return true, nil
		}
		if mark != "" {
			marks = append(marks, observedAdvance{topic: topic, ts: mark})
		}
		hadAny = hadAny || had
		if gi+1 < len(groups) && !steerConsumed {
			l.advanceFinished(chatJID, folder, msgs, groups[gi+1][0], marks)
		}
	}
	l.advance(chatJID, last)
	hadAny = hadAny || steerConsumed
	l.advanceObserved(folder, marks)
	return hadAny, nil
}

func (l *Loop) advanceFinished(chatJID, folder string, msgs []core.Message, first core.Message, marks []observedAdvance) {
	p := slices.IndexFunc(msgs, func(m core.Message) bool { return m.ID == first.ID })
	if p <= 0 {
		return
	}
	l.advance(chatJID, msgs[p-1])
	l.advanceObserved(folder, marks)
}

// advanceObserved moves the finished turns' observed watermarks. They ride the
// SAME success path as the message cursor: a re-fed turn must still see the
// context the failed attempt saw (BUGS L58).
func (l *Loop) advanceObserved(folder string, marks []observedAdvance) {
	for _, m := range marks {
		if err := l.db.store().AdvanceObservedCursor(folder, m.topic, m.ts); err != nil {
			slog.Error("observed cursor advance failed — the topic re-sees this window next turn",
				"folder", folder, "topic", m.topic, "ts", m.ts, "err", err)
		}
	}
}

func lostIDs(batch []core.Message) []string {
	out := make([]string, len(batch))
	for i, m := range batch {
		out[i] = m.ID
	}
	return out
}

func lostSenders(batch []core.Message) []string {
	var out []string
	for _, m := range batch {
		if m.Sender != "" && !slices.Contains(out, m.Sender) {
			out = append(out, m.Sender)
		}
	}
	return out
}

type observedAdvance struct{ topic, ts string }

// steerRowIdx picks the row `steer` is offered: the OLDEST slash command in
// the batch, else the newest non-bot row. Scanning the newest row only let a
// `/root` with any row after it reach the agent as plain text (BUGS L336); the
// oldest keeps the consume contract — rows behind it get their turn, rows
// after it stay above the cursor (BUGS L104).
func steerRowIdx(msgs []core.Message) int {
	for i, m := range msgs {
		if IsSteerCommand(m) {
			return i
		}
	}
	return newestUserIdx(msgs)
}

// newestUserIdx returns the position of the newest non-bot message, or -1 for
// an all-bot batch. The index, not the row: the rows behind it are the other
// half of the answer (BUGS L104).
func newestUserIdx(msgs []core.Message) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].BotMsg {
			return i
		}
	}
	return -1
}

func replyChat(chatJID string, last core.Message) string {
	if last.ForwardedFrom != "" {
		return last.ForwardedFrom
	}
	return chatJID
}

// isolateElevated cuts every batch around a row marked in pendingElevation, so
// the root turn holds only the /root issuer's row and its trigger is the
// issuer; another sender's row runs in its own unelevated turn (BUGS L666).
func (l *Loop) isolateElevated(groups [][]core.Message) [][]core.Message {
	var out [][]core.Message
	for _, g := range groups {
		start := 0
		for i, m := range g {
			if _, ok := l.pendingElevation.Load(m.ID); !ok {
				continue
			}
			if i > start {
				out = append(out, g[start:i])
			}
			out = append(out, g[i:i+1])
			start = i + 1
		}
		if start < len(g) {
			out = append(out, g[start:])
		}
	}
	return out
}

func (l *Loop) runBoundTurn(folder, topic, chatJID, turnID string, addressed bool, trigger, ambient []core.Message, saved *TurnContext, wakeCursor string) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	last := trigger[len(trigger)-1]

	elevated := false

	ctx := obs.WithTurn(context.Background(), l.instanceName, turnID)
	ctx, endSpan := obs.StartSpan(ctx, "turn",
		"folder", folder, "jid", chatJID, "turn_id", turnID)
	start := time.Now()
	defer func() {
		outcome := "success"
		if rerr != nil {
			outcome = "error"
		}
		obs.RecordTurn(folder, outcome, time.Since(start).Seconds())
		endSpan(rerr)
	}()

	if !l.db.GroupExists(folder) {
		slog.Error("dispatch refused: unregistered group", "folder", folder, "jid", chatJID, "turn_id", turnID)
		if err := l.publishStatus(TurnContext{TurnID: turnID, Folder: folder, ChatJID: chatJID, ReturnTo: last.ForwardedFrom, Topic: topic}, turnStatus{phase: "setup"}); err != nil {
			return false, false, false, "", err
		}
		return true, false, false, "", nil
	}
	inputKeys, err := turnMessageKeys(trigger)
	if err != nil {
		return false, false, false, "", err
	}
	oldest, err := l.oldestModelInput(folder, trigger[0])
	if err != nil {
		return false, false, false, "", err
	}
	if oldest.ID != "" && modelInputBefore(oldest, trigger[0]) {
		return false, false, false, "", errModelInputOrder
	}
	workKey := chatJID
	if saved != nil {
		workKey = deferredWorkPrefix + turnID
	}
	if !l.q.reserve(workKey, folder, core.IsScheduledSender(last.Sender), turnID) {
		return false, false, true, "", nil
	}
	defer l.q.release(workKey)
	ambientKeys, err := turnMessageKeys(ambient)
	if err != nil {
		return false, false, false, "", err
	}
	admission := store.TurnAdmission{AttemptID: randHex(16),
		InputLast: store.MessageCursor(last), InputKeys: inputKeys, AmbientKeys: ambientKeys}
	if l.media.Enabled {
		for i := range trigger {
			if err := l.enrichAttachments(ctx, &trigger[i], folder, true); err != nil {
				var deferred *attachmentDeferred
				if !errors.As(err, &deferred) {
					return false, false, false, "", err
				}
				if saved != nil {
					due := deferred.at
					if saved.NextAttemptAt.After(due) {
						due = saved.NextAttemptAt
					}
					if _, err := l.db.store().RequeueDeferredTurn(ctx, *saved, due, ""); err != nil {
						return false, false, false, "", err
					}
				}
				return false, false, true, "", nil
			}
		}
	}
	unlock := lockTurn(turnID)
	work, live, err := l.q.admit(l, TurnContext{TurnID: turnID, Folder: folder, Topic: topic,
		ChatJID: chatJID, Trigger: last.Sender, ReturnTo: last.ForwardedFrom,
		InputLast: admission.InputLast}, &admission, saved)
	unlock()
	if errors.Is(err, errTurnBlocked) {
		l.pauseProgress(replyChat(chatJID, last), topic, "")
		l.q.mu.Lock()
		l.q.getGroup(workKey).refused = true
		l.q.mu.Unlock()
		return false, false, true, "", nil
	}
	if err != nil || !live {
		return false, false, false, "", err
	}
	launched := false
	requeueAt := l.q.now()
	if saved != nil {
		defer func() {
			current, ok := l.db.GetTurnContext(turnID)
			if ok && current.State == store.TurnRunning && current.AttemptID == admission.AttemptID && !launched {
				_, err := l.db.store().RequeueDeferredTurn(ctx, current, requeueAt, "")
				if err != nil {
					rerr = err
				}
			}
		}()
	}

	if saved == nil || saved.RetryCount == 0 {
		for i := range trigger {
			if _, ok := l.pendingElevation.LoadAndDelete(trigger[i].ID); ok {
				elevated = true
			}
		}
	}
	tc := TurnContext{TurnID: turnID, Folder: folder, AttemptID: admission.AttemptID}
	started := false
	defer func() {
		if !started && errors.Is(rerr, ErrDependencyDown) {
			current, _ := l.db.GetTurnContext(turnID)
			_, err := l.q.complete(l, current, runedv1.RunOutcome{}, rerr, work)
			if err != nil {
				rerr = err
			}
		}
		if work.probe != nil && (!started || rerr == nil || errors.Is(rerr, store.ErrModelPaused)) {
			if err := l.q.rearm(l, *work.probe); err != nil {
				rerr = err
			}
		}
	}()

	l.emitSystemEvents(folder, chatJID)
	l.ensureTopicWithFork(folder, topic, l.forkParent(folder, last))
	rendered, observedMark := l.buildBoundPrompt(folder, topic, trigger, ambient, saved != nil)

	if tc, ok := l.db.GetTurnContext(turnID); ok && tc.RetryCount > 0 {
		rendered = retryNoteBlock(tc.RetryCount) + rendered
	}

	if err := l.db.SetTurnAddressed(turnID, addressed); err != nil {
		return false, false, false, "", err
	}

	_ = l.db.SetTurnTriggerMsg(turnID, last.ID)
	_ = l.db.SetLastReply(chatJID, topic, last.ID, folder)

	if l.srv != nil {
		if ipcDir, ierr := l.folders.IpcPath(folder); ierr == nil {
			if stop, serr := l.srv.ServeTurnMCP(turnMCP{
				folder: folder, topic: topic, chatJID: chatJID, turnID: turnID, trigger: last.Sender, elevated: elevated,
				attempt: tc.AttemptID,
			}, ipcDir); serr == nil {
				defer stop()
			} else {
				slog.Warn("serve turn mcp", "folder", folder, "turn_id", turnID, "err", serr)
			}
		}
	}

	if l.folders != nil && l.folders.IpcDir != "" {
		container.WriteTasksSnapshot(l.folders, folder, elevated, l.db.Tasks(folder, elevated))
		container.WriteGroupsSnapshot(l.folders, folder, elevated, slices.Collect(maps.Values(l.db.AllGroups())))
	}

	if !core.IsIsolatedSender(last.Sender) {
		if sid := l.db.SessionID(folder, topic); sid != "" && l.sessionIdleExpired(chatJID) {
			slog.Info("session: resetting on idle expiry",
				"folder", folder, "topic", topic, "jid", chatJID, "threshold", l.sessionIdle)
			_ = l.db.DeleteSession(folder, topic)
		}
	}

	// The turn's own virtual key at the model gateway (spec 6/23): TTL = the run
	// ceiling, attributed to the folder and the caller, revoked by the deferred
	// delete. The alias ends in random hex because a retry re-dispatches the
	// SAME turn_id. The gateway's team and caps are reconciled first, since the
	// mint needs the team. A gateway that will not answer the reconcile or the
	// mint is a dependency outage: no guest starts.
	if err := l.reconcileCaps(ctx, folder, turnCallerSub(last.Sender)); err != nil {
		slog.Error("model gateway did not answer the cap reconcile; no spawn",
			"folder", folder, "turn_id", turnID, "err", err)
		return false, false, false, "", fmt.Errorf("%w: model gateway cap reconcile: %w",
			ErrDependencyDown, err)
	}
	var gwKey turnKey
	if l.gateway != nil {
		k, gerr := l.gateway.mint(ctx, "arizuko-"+turnID+"-"+randHex(4),
			folder, turnID, turnCallerSub(last.Sender), l.runTimeout)
		if gerr != nil {
			slog.Error("model gateway refused to mint this turn's key; no spawn",
				"folder", folder, "turn_id", turnID, "err", gerr)
			return false, false, false, "", fmt.Errorf("%w: model gateway mint: %w",
				ErrDependencyDown, gerr)
		}
		gwKey = k

		if err := l.db.SetTurnGatewayKey(turnID, gwKey.Hash); err != nil {
			slog.Error("turn's gateway key not recorded; its cost becomes unreadable",
				"folder", folder, "turn_id", turnID, "err", err)
		}
		defer func() {
			if err := l.gateway.revoke(ctx, gwKey.Key); err != nil {
				slog.Error("model gateway key not revoked; it stands until its TTL expires",
					"folder", folder, "turn_id", turnID, "err", err)
			}
		}()
	}

	slog.Info("dispatch run", "folder", folder, "topic", topic, "turn_id", turnID,
		"chat_jid", chatJID, "trigger", last.Sender)

	replyJID := replyChat(chatJID, last)
	l.waiting.Delete(chatJID)
	l.beginProgress(replyJID, topic)

	progress := addressed && canShowProgress(l.deliver, replyJID)
	currentStatus, _ := l.db.GetTurnContext(turnID)
	if err := l.publishOwedStatus(currentStatus); err != nil {
		return false, false, false, "", err
	}
	started = true
	launched = true
	out, derr := l.dispatchRun(ctx, folder, topic, replyJID, turnID, last.Sender, rendered, elevated, progress, gwKey.Key)
	closed, closedOK := l.db.GetTurnContext(turnID)
	if closedOK && closed.State == store.TurnDone && closed.RunReturned {
		finishTurn(l.db, l.deliver, turnID, endSteered)
		return false, false, false, "", nil
	}
	tc.RunID = out.RunID
	if out.RunID != "" {
		_ = l.db.SetTurnRunID(turnID, out.RunID)
	}
	if derr != nil {
		l.pauseProgress(replyJID, topic, out.RunID)
		if err := l.db.SetRunReturned(turnID); err != nil {
			return false, false, false, "", err
		}
		current, _ := l.db.GetTurnContext(turnID)
		if !errors.Is(derr, context.Canceled) {
			if _, err := l.q.interrupt(l, current); err != nil {
				return false, false, false, "", err
			}
			var apiErr *runedv1.APIError
			if current.ProviderFailureAttempt == tc.AttemptID ||
				(!runedUnreachable(derr) && !errors.As(derr, &apiErr)) {
				if err := l.stopModelProbe(current); err != nil {
					return false, false, false, "", err
				}
			}
		}
		if _, err := l.q.complete(l, current, out, derr, work); err != nil {
			return false, false, false, "", err
		}
		if errors.Is(derr, context.Canceled) || errors.Is(derr, context.DeadlineExceeded) {
			finishTurn(l.db, l.deliver, turnID, endFailed)
			return false, false, false, "", nil
		}
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		if runedUnreachable(derr) {
			derr = fmt.Errorf("%w: %w", ErrDependencyDown, derr)
		}
		return false, false, false, "", derr
	}
	if out.Busy {
		current, ok, err := l.db.store().ReadTurnContext(ctx, turnID)
		if err != nil {
			return false, false, false, "", err
		}
		if !ok || current.AttemptID != admission.AttemptID {
			return false, false, false, "", store.ErrRefused
		}
		decision := retry.Next(current.CapacityFailures, retry.Failure{Kind: "transient"}, l.q.now(), time.Time{})
		committed, err := l.db.store().RecordCapacityFailure(ctx, current, l.db.GetAgentCursor(chatJID), wakeCursor, decision)
		if err != nil {
			return false, false, false, "", err
		}
		if !committed {
			return false, false, false, "", store.ErrRefused
		}
		// A capacity wait that will re-feed keeps typing on (BUGS L897); a
		// stop or a saved replay clears it.
		if decision.Stop || saved != nil {
			l.pauseProgress(replyJID, topic, out.RunID)
		} else {
			l.waiting.Store(chatJID, presence{replyJID, topic})
		}
		if decision.Stop {
			current, _, err = l.db.store().ReadTurnContext(ctx, turnID)
			if err != nil {
				return false, false, false, "", err
			}
			if err := l.publishOwedStatus(current); err != nil {
				return false, false, false, "", err
			}
		}
		return false, false, true, "", nil
	}
	if out.Steered {

		finishTurn(l.db, l.deliver, turnID, endSteered)
		_ = l.db.SetRunReturned(turnID)
		return true, true, false, "", nil
	}

	_ = l.db.SetRunReturned(turnID)
	current, exists, err := l.db.store().ReadTurnContext(ctx, turnID)
	if err != nil {
		return false, false, false, "", err
	}
	if !exists {
		return false, false, false, "", fmt.Errorf("claimed turn %s disappeared", turnID)
	}
	deferred, err := l.q.complete(l, current, out, nil, work)
	if err != nil {
		return false, false, false, "", err
	}
	if deferred {
		l.pauseProgress(replyJID, topic, out.RunID)
		return false, false, false, "", nil
	}
	if out.Outcome == runedv1.OutcomeError {
		tc, _ := l.db.GetTurnContext(turnID)

		slog.Warn("run outcome error", "folder", folder, "turn_id", turnID, "err", out.Error,
			"retry_count", tc.RetryCount)
		_ = l.db.MarkMessagesErrored(trigger)
		for _, jid := range l.db.chatsOwnedBy(returnTarget(tc, chatJID), out.RunID) {
			if err := l.db.MarkChatErrored(jid); err != nil {
				slog.Error("flag chat errored", "jid", jid, "err", err)
			}
		}
		finishTurn(l.db, l.deliver, turnID, endFailed)
		return false, false, false, "", nil
	}
	end := endDone
	result, err := l.db.store().GetTurnResult(folder, turnID)
	if err != nil {
		return false, false, false, "", err
	}
	switch result.Status {
	case "error":
		end = endFailed
	case "success":
		for _, jid := range l.db.chatsOwnedBy(chatJID, out.RunID) {
			_ = l.db.ClearChatErrored(jid)
		}
	}
	recorded := result.Status != "pending"

	if out.Outcome == runedv1.OutcomeOK && !recorded && !l.db.TurnHasBotReply(chatJID, turnID) {
		slog.Error("silent turn: clean run delivered nothing",
			"folder", folder, "turn_id", turnID, "jid", chatJID)
		obs.RecordSilentTurn(folder)
		end = endFailed
		current, _ := l.db.GetTurnContext(turnID)
		if err := l.publishStatus(current, turnStatus{phase: "silent"}); err != nil {
			return false, false, false, "", err
		}
	}
	finishTurn(l.db, l.deliver, turnID, end)
	return out.Outcome != runedv1.OutcomeSilent, false, false, observedMark, nil
}

func turnMessageKeys(messages []core.Message) ([]objstore.Key, error) {
	keys := make([]objstore.Key, 0, len(messages))
	for _, message := range messages {
		key, err := store.MessageKey(message.ChatJID, core.TS(message.Timestamp), message.ID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (l *Loop) forkParent(folder string, last core.Message) string {
	if last.ReplyToID == "" {
		return ""
	}
	parent := l.db.TopicByID(last.ChatJID, last.ReplyToID)
	if _, ok := l.db.GetSession(folder, parent); !ok {
		return ""
	}
	return parent
}

func (l *Loop) ensureTopicWithFork(folder, topic, parent string) {
	childUUID := core.NewSessionID()
	inserted, err := l.db.EnsureTopicLineage(folder, topic, parent, childUUID)
	if err != nil {
		slog.Warn("ensureTopicWithFork: lineage insert failed",
			"folder", folder, "topic", topic, "err", err)
		return
	}
	if !inserted {
		return
	}
	l.inheritParentSession(folder, parent, topic, childUUID)
}

// inheritParentSession copies the parent's transcript under childUUID or, when
// it cannot, clears the child's observed cursor. Both halves are one rule: the
// fork's observed_cursor=now stamp is right ONLY while a transcript carries
// the rest (BUGS L54). Every fork path goes through here.
func (l *Loop) inheritParentSession(folder, parent, childTopic, childUUID string) {
	if l.copyParentSession(folder, parent, childUUID) {
		return
	}
	if err := l.db.store().ClearObservedCursor(folder, childTopic); err != nil {
		slog.Warn("inheritParentSession: clearing the observed cursor failed — the child topic starts with no context",
			"folder", folder, "topic", childTopic, "err", err)
	}
}

func (l *Loop) copyParentSession(folder, parent, childUUID string) bool {
	parentUUID, ok := l.db.GetSession(folder, parent)
	if !ok || parentUUID == "" {
		return false
	}
	groupDir, err := l.folders.GroupPath(folder)
	if err != nil {
		slog.Warn("copyParentSession: group path", "folder", folder, "err", err)
		return false
	}
	copied, err := container.CopySession(groupDir, parentUUID, childUUID)
	if err != nil {
		slog.Warn("copyParentSession: cp failed",
			"folder", folder, "parent", parent,
			"parentUUID", parentUUID, "childUUID", childUUID, "err", err)
		return false
	}
	return copied
}

func consecutiveRuns(msgs []core.Message, key func(core.Message) string) [][]core.Message {
	if len(msgs) == 0 {
		return nil
	}
	var batches [][]core.Message
	for i, m := range msgs {
		if i == 0 || key(m) != key(msgs[i-1]) {
			batches = append(batches, nil)
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], m)
	}
	return batches
}

func groupByTopic(msgs []core.Message) [][]core.Message {
	return consecutiveRuns(msgs, func(m core.Message) string { return m.Topic })
}

func retryNoteBlock(attempt int) string {
	return fmt.Sprintf("<system-note>This is retry attempt %d. "+
		"The previous attempt did not complete.</system-note>\n", attempt)
}

func jidScheme(jid string) string {
	if s, _, ok := strings.Cut(jid, ":"); ok {
		return s
	}
	return ""
}

// turnCallerSub is the ONE rule for "who is this turn acting on behalf of":
// spawn-time secrets, connector secrets, the budget gate and cost attribution
// all read it. A timed or system trigger resolves to routd itself; every other
// sender passes VERBATIM — filtering by scheme empties `user_sub` in cost_log
// (BUGS L69). It is NOT the tool-call authorization principal, which is
// folder:<path> (BUGS B2).
func turnCallerSub(trigger string) string {
	if isSystemTrigger(trigger) {
		return "service:routd"
	}
	return trigger
}

func isSystemTrigger(trigger string) bool {

	return trigger == "" || trigger == "timed" || strings.HasPrefix(trigger, "timed-") || strings.HasPrefix(trigger, "system")
}

// runedUnreachable reports whether routd never reached runed: nothing ran, so
// the failure is boot order or a restart, not a verdict on the folder. An
// APIError is a refusal runed answered, which IS attributable; a mid-run
// deadline is also a bare error but the run DID start, so this stays narrow.
func runedUnreachable(err error) bool {
	var apiErr *runedv1.APIError
	if errors.As(err, &apiErr) {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}

	return errors.Is(err, syscall.ECONNREFUSED)
}

func (l *Loop) dispatchRun(ctx context.Context, folder, topic, chatJID, turnID, trigger, batch string, elevated, progress bool, gatewayKey string) (runedv1.RunOutcome, error) {
	var cancel context.CancelFunc
	if l.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.runTimeout)
		defer cancel()
	}
	caller := types.UserSub(turnCallerSub(trigger))

	isolated := core.IsIsolatedSender(trigger)
	sessionID := ""
	if !isolated {
		sessionID = l.db.SessionID(folder, topic)

		if sessionID == "" {
			sessionID = core.NewSessionID()
			if err := l.db.PutSession(folder, topic, sessionID); err != nil {
				return runedv1.RunOutcome{}, fmt.Errorf("persist the session of %s#%s: %w", folder, topic, err)
			}
		}
	}
	harness, agentValue, containerCfg, err := l.db.store().GroupConfig(folder)
	if err != nil {
		return runedv1.RunOutcome{}, err
	}
	agent, err := store.ParseGroupAgent(agentValue)
	if err != nil {
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true,
			Error: fmt.Sprintf("group %q agent %s: %v", folder, agentValue, err)}, nil
	}

	allowlist, _ := l.db.ResolveAllowlist(folder)

	secrets := l.db.EnvProfileSecrets(string(caller))

	shellEnv := l.db.ShellEnvSecrets(folder, string(caller))
	maps.Copy(secrets, shellEnv)

	sub := "folder:" + folder
	shareRO := !elevated && l.db.Authorize(sub, folder, "mcp:share_mount", map[string]string{"readonly": "true"})
	egress := elevated || l.db.Authorize(sub, folder, "egress", nil)
	webPublish := elevated || l.db.Authorize(sub, folder, "web:publish", nil)
	ghWrite := elevated || l.db.Authorize(sub, folder, "gh:write", nil)

	slog.Info("dispatch capabilities resolved",
		"folder", folder, "turn_id", turnID, "elevated", elevated,
		"egress", egress, "share_readonly", shareRO, "web_publish", webPublish,
		"gh_write", ghWrite,
		"shell_env", slices.Sorted(maps.Keys(shellEnv)))
	packages, managed, err := l.db.SelectedPackages(folder)
	if err != nil {
		return runedv1.RunOutcome{}, err
	}
	return l.runner.Run(ctx, runedv1.RunRequest{
		Agent:        agent,
		Packages:     packages,
		ManagedFiles: managed,
		Folder:       types.Folder(folder),
		Topic:        topic,
		ChatJID:      chatJID,

		Channel:         jidScheme(chatJID),
		SessionID:       sessionID,
		MessageBatch:    batch,
		TriggerSender:   trigger,
		CallerSub:       caller,
		TurnID:          turnID,
		ContainerConfig: containerCfg,
		Harness:         harness,
		Isolated:        isolated,
		Elevated:        elevated,
		Progress:        progress,
		ShareReadOnly:   shareRO,
		Egress:          egress,
		WebPublish:      webPublish,
		GHWrite:         ghWrite,
		EgressAllowlist: allowlist,
		Secrets:         secrets,

		ModelGatewayKey: gatewayKey,
	})
}
