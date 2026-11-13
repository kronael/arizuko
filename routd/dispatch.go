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
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/types"
)

func (l *Loop) processGroupMessages(chatJID string) (bool, error) {
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
	// Steering consumes the latest message BEFORE it reaches a turn, on the one
	// path every trigger converges to. steerConsumed says this batch held a
	// command; runTurn's `steered` is a different fact about a different row.
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
	if deferredSteer == nil && !steerConsumed && l.q.busyDeferred(chatJID, msgs[busyIdx].ID) {
		return false, nil
	}
	// Bot rows stay out of the trigger but in the rendered context. A user row
	// that does not dispatch on its own is observed under the batch folder before
	// the prompt is built, so this turn renders it as context.
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

	// Both topic overrides are properties of the CHAT, resolved once: the key
	// function below runs per row and pollBatch is 200.
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
	// Applied only with the cursor advance past the turns that set them (BUGS L58).
	var marks []observedAdvance
	for gi, batch := range groups {
		bl := batch[len(batch)-1]
		topic := turnTopic(bl)
		// A person is owed an answer by the turn that holds their row, however many
		// unaddressed rows (CI under ?engaged) run beside it in their own turns.
		owed := slices.ContainsFunc(batch, func(m core.Message) bool { return addressed[m.ID] })
		turnID := bl.ID
		had, steered, busy, mark, derr := l.runTurn(folder, topic, chatJID, turnID, owed, batch, ambient)
		if errors.Is(derr, errModelInputOrder) {
			if gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			return hadAny, l.holdModelInput(chatJID, batch)
		}
		if errors.Is(derr, store.ErrModelPaused) {
			if gi > 0 {
				l.advanceFinished(chatJID, folder, msgs, batch[0], marks)
			}
			return hadAny, l.holdModelInput(chatJID, batch)
		}
		if derr != nil {
			// Transport failure: this turn is not consumed and the queue re-feeds
			// it after a backoff (at-least-once; turn_results dedups). A steered
			// batch has no re-feed left: its cursor already passed the command.
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
			// This turn re-feeds after the busy backoff.
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
			// Steer ack: the original run governs the batch; don't advance.
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

// advanceFinished moves the cursor to the row just before first, the first row
// of the turn that was not consumed, when the turns before it finished. A
// re-feed must not decide finished rows again: a finished turn can open
// engagement, a row that was context then dispatches, the group grows, its
// turn id changes, and the done check misses.
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

// lostIDs and lostSenders identify rows that will not be re-fed: the ids and
// the senders, never the content — a dropped message is still the sender's.
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

// observedAdvance is one topic's pending observed watermark for this dispatch.
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

// replyChat is the chat that RECEIVES a turn's reply: the origin for a
// forwarded row.
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

// runTurn dispatches ONE turn for a bot-stripped trigger batch and records its
// outcome. It never advances the agent_cursor; busy=true means the batch was
// not consumed and the caller must re-feed it, not advance.
func (l *Loop) runTurn(folder, topic, chatJID, turnID string, addressed bool, trigger, ambient []core.Message) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	return l.runBoundTurn(folder, topic, chatJID, turnID, addressed, trigger, ambient, nil)
}

func (l *Loop) runBoundTurn(folder, topic, chatJID, turnID string, addressed bool, trigger, ambient []core.Message, saved *TurnContext) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	last := trigger[len(trigger)-1]
	// Elevation is one-shot and consumed here, so a re-fed batch never
	// re-elevates; the whole batch is scanned so batching with a sibling cannot
	// drop the signal.
	elevated := false
	// The trace id derives from the turn id so every daemon handling this turn
	// shares one trace (spec 5/O-observability).
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
	// A rule can point at a folder with no group row, and a config-less spawn
	// never replies: refuse loud, tell the chat, consume the batch.
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
	unlock := lockTurn(turnID)
	work, live, err := l.q.admit(l, TurnContext{TurnID: turnID, Folder: folder, Topic: topic,
		ChatJID: chatJID, Trigger: last.Sender, ReturnTo: last.ForwardedFrom,
		InputLast: admission.InputLast}, &admission, saved)
	unlock()
	if errors.Is(err, errTurnBlocked) {
		l.pauseProgress(replyChat(chatJID, last), "")
		l.q.mu.Lock()
		l.q.getGroup(workKey).refused = true
		l.q.mu.Unlock()
		return false, false, true, "", nil
	}
	if err != nil || !live {
		return false, false, false, "", err
	}
	launched := false
	requeueAt := l.q.now().Add(busyBackoffBase)
	if saved != nil {
		defer func() {
			current, ok := l.db.GetTurnContext(turnID)
			if ok && current.State == store.TurnRunning && current.AttemptID == admission.AttemptID && !launched {
				_, err := l.db.store().RequeueDeferredTurn(ctx, current, requeueAt)
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
	// A gateway that will not answer fails the turn as a dependency outage (the
	// batch re-feeds); spawning anyway would
	// run a turn no cap can stop.
	msg, gerr := l.budgetGate(ctx, folder, turnCallerSub(last.Sender))
	if gerr != nil {
		slog.Error("model gateway did not answer the budget gate; no spawn",
			"folder", folder, "turn_id", turnID, "err", gerr)
		return false, false, false, "", fmt.Errorf("%w: model gateway budget gate: %w",
			ErrDependencyDown, gerr)
	}
	// A spent cap delivers a refusal and consumes the batch.
	if msg != nil {
		current, _ := l.db.GetTurnContext(turnID)
		if err := l.publishStatus(current, *msg); err != nil {
			return false, false, false, "", err
		}
		finishTurn(l.db, l.deliver, turnID, endDone)
		return true, false, false, "", nil
	}
	// BEFORE the prompt build, so FlushSysMsgs renders them this turn.
	l.emitSystemEvents(folder, chatJID)
	l.ensureTopicWithFork(folder, topic, l.forkParent(folder, last))
	// Enrichment rewrites the trigger rows and persists them, so later turns'
	// observed context sees the transcript too.
	if l.media.Enabled {
		ctx := context.Background()
		for i := range trigger {
			l.enrichAttachments(ctx, &trigger[i], folder)
		}
	}
	rendered, observedMark := l.buildBoundPrompt(folder, topic, trigger, ambient, saved != nil)
	// The turn_context row is the failed prior attempt's (spec 5/12).
	if tc, ok := l.db.GetTurnContext(turnID); ok && tc.RetryCount > 0 {
		rendered = retryNoteBlock(tc.RetryCount, l.maxTurnRetry) + rendered
	}

	// forwarded_from is the turn's return address: reply/send/document deliver
	// to the origin chat, not the child folder JID the run addresses.
	if err := l.db.SetTurnAddressed(turnID, addressed); err != nil {
		return false, false, false, "", err
	}
	// A threaded reply can root its platform thread on the trigger.
	_ = l.db.SetTurnTriggerMsg(turnID, last.ID)
	_ = l.db.SetLastReply(chatJID, topic, last.ID, folder)

	// The socket lives for the whole synchronous run; routd and runed agree on
	// the path via folders.IpcPath.
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

	// The all-groups/all-tasks view is a root privilege: it tracks elevation,
	// NOT folder shape. With no ipc dir (REST/unit tests) the write would
	// resolve relative to cwd, so skip it.
	if l.folders != nil && l.folders.IpcDir != "" {
		container.WriteTasksSnapshot(l.folders, folder, elevated, l.db.Tasks(folder, elevated))
		container.WriteGroupsSnapshot(l.folders, folder, elevated, slices.Collect(maps.Values(l.db.AllGroups())))
	}

	// A chat idle past the threshold would resume a session the agent has long
	// forgotten. Isolated runs carry no session lineage.
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
	// SAME turn_id. A gateway that will not mint is a dependency outage: the
	// batch re-feeds without starting a guest.
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
		// GET /v1/cost?turn_id= resolves this. Recorded before the spawn: a routd
		// that dies mid-run must still leave the turn pointing at its key.
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
	// The chat that RECEIVES the reply: the origin for a forwarded turn. It
	// types there and the run takes its output style from it.
	replyJID := replyChat(chatJID, last)
	l.waiting.Delete(chatJID) // the run owns the indicator now
	l.beginProgress(replyJID)
	// The agent nudges for a progress line only where a person is owed an answer
	// and the reply chat can show and remove one.
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
		l.pauseProgress(replyJID, out.RunID)
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
		requeueAt = l.q.noteBusy(chatJID, last.ID)
		launched = false
		started = false
		if saved != nil {
			l.pauseProgress(replyJID, out.RunID)
			return false, false, true, "", nil
		}
		// Typing stays on: the batch re-feeds. End or a drop of the chat clears it.
		l.waiting.Store(chatJID, replyJID)
		// runed keeps no internal queue: leave turn_context running and the
		// cursor un-advanced.
		time.AfterFunc(requeueAt.Sub(l.q.now()), func() { l.q.EnqueueMessageCheck(chatJID) })
		slog.Debug("dispatch run busy — runed at capacity, retry after backoff", "folder", folder, "turn_id", turnID)
		return false, false, true, "", nil
	}
	if out.Steered {
		// The original run owns the batch and submits under ITS turn_id. THIS
		// turn is done, or a re-fed poll / recoverPending re-spawns it and
		// duplicates the output.
		finishTurn(l.db, l.deliver, turnID, endSteered)
		_ = l.db.SetRunReturned(turnID)
		return true, true, false, "", nil
	}
	// Close the callback surface so a late frame 409s, even if an early
	// submit_turn already flipped state→done.
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
		l.pauseProgress(replyJID, out.RunID)
		return false, false, false, "", nil
	}
	if out.Outcome == runedv1.OutcomeError {
		tc, _ := l.db.GetTurnContext(turnID)
		// Final failure: retries exhausted, or a partial reply exists.
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
	// Deliberate silence still records a result, so this fires only on a
	// malfunction: a 0-result SDK run, a config-less spawn, a swallowed
	// submit_turn. Not transient — never retry; tell the chat.
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

// forkParent answers the topic a new topic forks lineage from: the topic of
// the message last replies to when that topic has a session, else main (""). A
// thread a reply rooted on a main-topic turn has the thread's own topic on the
// bot row it is answered under, and that topic has no session until this turn
// creates one.
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

// ensureTopicWithFork ensures (folder, topic) has a sessions row and, when
// newly inserted, copies the parent topic's session so the child resumes from
// its tail. Failures never block the turn; parent="" forks from main.
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

// copyParentSession reports whether the child now owns a resumable transcript:
// false when the parent has no session yet or the copy failed.
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

// consecutiveRuns splits msgs into consecutive same-key runs: A,B,A yields
// [A],[B],[A]. Regrouping the whole slice by key would reorder the conversation.
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

// retryNoteBlock renders the system note injected on retry attempts.
func retryNoteBlock(attempt, max int) string {
	return fmt.Sprintf("<system-note>This is retry attempt %d of %d. "+
		"The previous attempt did not complete.</system-note>\n", attempt, max)
}

// jidScheme returns the JID scheme — the per-surface output-style selector —
// or "" for a scheme-less JID, which maps to the default style.
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

// isSystemTrigger reports whether a trigger sender is a task or the system, not
// a person. The ONE predicate: turnCallerSub and the turn's addressed fact
// (spec 6/47 §"Addressed") both read it.
func isSystemTrigger(trigger string) bool {
	// The scheduler sends a plain "timed" for a group-context task, which does
	// not carry the "timed-" prefix.
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
	// No A record yet: compose brings runed up after routd.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// The name resolves but nothing listens on :8080 yet.
	return errors.Is(err, syscall.ECONNREFUSED)
}

// dispatchRun renders the run request and calls runed POST /v1/runs. The
// conversation frames arrive out-of-band via the callbacks; this returns the
// turn-boundary outcome.
func (l *Loop) dispatchRun(ctx context.Context, folder, topic, chatJID, turnID, trigger, batch string, elevated, progress bool, gatewayKey string) (runedv1.RunOutcome, error) {
	var cancel context.CancelFunc
	if l.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.runTimeout)
		defer cancel()
	}
	caller := types.UserSub(turnCallerSub(trigger))
	// An isolated scheduled task runs with NO `--resume` (ROUTING.md § Topics isolate threads inside one group): resuming
	// the chat session appends the cron's tool-heavy run to the humans'
	// conversation, and compaction drops the humans first.
	isolated := core.IsIsolatedSender(trigger)
	sessionID := ""
	if !isolated {
		sessionID = l.db.SessionID(folder, topic)
		// Declared and persisted BEFORE the run, never learned from it: a turn
		// killed before submit_turn — a restart, a deadline, a crash — reports
		// nothing, and the re-fed turn must still resume the transcript the
		// killed attempt wrote. The harness creates its session under this id.
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
	// routd is the authz plane; runed has neither store. nil allowlist on error
	// is fine — runed treats empty as no extra constraint.
	allowlist, _ := l.db.ResolveAllowlist(folder)
	// Container env carries the caller's OWN model credentials (BYOA). An
	// UNGRANTED folder credential reaches a tool only through the MCP broker,
	// per call (spec 5/13 §Trust model). routd decrypts (it holds SECRETS_KEY);
	// runed injects.
	secrets := l.db.EnvProfileSecrets(string(caller))
	// The third tier (5/13 §Trust model): an explicit `shell:env:<KEY>` grant
	// makes a folder credential readable from the agent's own shell, because a
	// CLI the agent drives has no tool call to broker.
	shellEnv := l.db.ShellEnvSecrets(folder, string(caller))
	maps.Copy(secrets, shellEnv)
	// mounts/egress/web are GRANTS, not tier (5/33-paths-roles): runed consumes a decision,
	// not a rule bundle. An elevated turn holds `*`; role:member grants none.
	sub := "folder:" + folder
	shareRO := !elevated && l.db.Authorize(sub, folder, "mcp:share_mount", map[string]string{"readonly": "true"})
	egress := elevated || l.db.Authorize(sub, folder, "egress", nil)
	webPublish := elevated || l.db.Authorize(sub, folder, "web:publish", nil)
	ghWrite := elevated || l.db.Authorize(sub, folder, "gh:write", nil)
	// This line IS the signal for a denied capability: web:publish=false makes
	// the runner skip the web mounts and the agent reports a broken mount;
	// without it the host has nothing to grep.
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
		// The JID scheme picks the per-surface output style; a scheme-less JID
		// yields "" → default. SplitN would hand back the folder path as a
		// style name, so jidScheme.
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
		// routd holds what mints it; runed injects what it is given.
		ModelGatewayKey: gatewayKey,
	})
}
