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
	"syscall"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/queue"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// processGroupMessages is the queue's per-folder-serialized worker: it renders
// the batch, dispatches one run to runed and records the outcome. Returns
// (hadOutput, err) per the queue's processMessagesFn.
func (l *Loop) processGroupMessages(chatJID string) (bool, error) {
	cursor := l.db.GetAgentCursor(chatJID)
	msgs, err := l.db.NewMessagesForChat(chatJID, cursor, pollBatch)
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
	// (BUGS L56, L336), and a newer review row that matched only #observe took a
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
	// The busy key is the newest USER row: a person's next message clears the
	// backoff, the running turn's own bot rows do not (BUGS L298, L55).
	if l.busyDeferred(chatJID, msgs[busyIdx].ID) {
		return false, nil
	}
	b := l.resolveBatch(chatJID, msgs)
	if !b.ok {
		l.routeMiss(chatJID, msgs, b.resolution)
		return false, nil
	}
	r := b.resolution
	folder := r.Folder
	// Steering consumes the latest message BEFORE it reaches a turn, on the one
	// path every trigger converges to; in the pollOnce backstop alone it raced
	// the queue and lost. steerConsumed says this batch held a command; runTurn's
	// `steered` is a different fact about a different row.
	steerConsumed := false
	steerIdx := steerRowIdx(msgs)
	steerMsg := msgs[steerIdx]
	if l.steer(chatJID, steerMsg, folder) {
		// Consumed HERE, exactly once, before anything else can fail: no steer
		// handler may run a second time because a later dispatch was refused.
		l.advance(chatJID, steerMsg)
		// A steer consumes its own row and no other: the older rows still get the
		// turn below (BUGS L104), and `last` stays at the steered row so the
		// closing advance does not walk the cursor back over the command.
		steerConsumed = true
		msgs, last = msgs[:steerIdx], steerMsg
	}
	// Bot rows stay out of the trigger but in the rendered context. A user row
	// that does not dispatch on its own is observed under the batch folder before
	// the prompt is built, so this turn renders it as context.
	var trigger, ambient []core.Message
	for i, m := range msgs {
		switch {
		case m.BotMsg:
		case b.dispatches[i]:
			trigger = append(trigger, m)
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
	pin := l.effectiveTopic(chatJID, "")
	if !web && r.Topic != "" {
		pin = r.Topic
	}
	turnTopic := func(m core.Message) string {
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

	hadAny := false
	// Applied only if this dispatch reaches the advance below (BUGS L58).
	var marks []observedAdvance
	for _, batch := range groups {
		bl := batch[len(batch)-1]
		topic := turnTopic(bl)
		had, steered, busy, mark, derr := l.runTurn(folder, topic, chatJID, bl.ID, r.Addressed, batch)
		if derr != nil {
			// Transport failure: do NOT advance — re-fed next poll (at-least-once;
			// turn_results dedups). A steered batch has no re-feed left: its cursor
			// already passed the command.
			if steerConsumed {
				slog.Error("the rows a steer left behind did not reach runed; they are not re-fed",
					"jid", chatJID, "rows", len(batch), "ids", lostIDs(batch),
					"senders", lostSenders(batch), "err", derr)
			}
			return hadAny, derr
		}
		if busy {
			// Not consumed: do NOT advance and do NOT return an err (it would trip
			// the queue breaker); the batch re-feeds after the busy backoff.
			l.noteBusy(chatJID, steerMsg.ID)
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
	}
	l.advance(chatJID, last)
	hadAny = hadAny || steerConsumed
	// The observed watermark rides the SAME success path as the message cursor:
	// a re-fed turn must still see the context the failed attempt saw (BUGS L58).
	for _, m := range marks {
		if err := l.db.AdvanceObservedCursor(folder, m.topic, m.ts); err != nil {
			slog.Error("observed cursor advance failed — the topic re-sees this window next turn",
				"folder", folder, "topic", m.topic, "ts", m.ts, "err", err)
		}
	}
	return hadAny, nil
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

// Busy re-dispatch backoff: doubles per consecutive refusal of the same batch,
// capped so a starved chat probes runed once a minute, not once per poll
// (BUGS L55).
const (
	busyBackoffBase = 4 * time.Second
	busyBackoffCap  = 60 * time.Second
)

// busyState is one chat's re-dispatch backoff after a runed Busy refusal.
type busyState struct {
	until time.Time
	delay time.Duration
	// A different newest id means fresh inbound activity: the backoff no
	// longer applies.
	lastMsgID string
}

// busyDeferred reports whether the chat's batch is inside its busy backoff
// window. A changed newest-message id clears the backoff; an expired window
// keeps the entry so a repeat refusal doubles.
func (l *Loop) busyDeferred(chatJID, lastMsgID string) bool {
	v, ok := l.busyWait.Load(chatJID)
	if !ok {
		return false
	}
	st := v.(busyState)
	if st.lastMsgID != lastMsgID {
		l.busyWait.Delete(chatJID)
		return false
	}
	return time.Now().Before(st.until)
}

// noteBusy records a runed Busy refusal for the chat's current batch, doubling
// the backoff on a consecutive refusal of the same batch.
func (l *Loop) noteBusy(chatJID, lastMsgID string) {
	delay := busyBackoffBase
	if v, ok := l.busyWait.Load(chatJID); ok {
		if st := v.(busyState); st.lastMsgID == lastMsgID {
			delay = min(st.delay*2, busyBackoffCap)
		}
	}
	l.busyWait.Store(chatJID, busyState{
		until:     time.Now().Add(delay),
		delay:     delay,
		lastMsgID: lastMsgID,
	})
}

// runTurn dispatches ONE turn for a bot-stripped trigger batch and records its
// outcome. It never advances the agent_cursor; busy=true means the batch was
// not consumed and the caller must re-feed it, not advance.
func (l *Loop) runTurn(folder, topic, chatJID, turnID string, addressed bool, trigger []core.Message) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	last := trigger[len(trigger)-1]
	// Elevation is one-shot and consumed here, so a re-fed batch never
	// re-elevates; the whole batch is scanned so batching with a sibling cannot
	// drop the signal.
	elevated := false
	for i := range trigger {
		if _, ok := l.pendingElevation.LoadAndDelete(trigger[i].ID); ok {
			elevated = true
		}
	}
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
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, ghostGroupNotice, "", topic, "", "ghost-"+turnID)
		}
		return true, false, false, "", nil
	}
	// A gateway that will not answer fails the turn as a dependency outage (the
	// batch re-feeds, the breaker streak is untouched); spawning anyway would
	// run a turn no cap can stop.
	msg, gerr := l.budgetGate(ctx, folder, turnCallerSub(last.Sender))
	if gerr != nil {
		slog.Error("model gateway did not answer the budget gate; no spawn",
			"folder", folder, "turn_id", turnID, "err", gerr)
		return false, false, false, "", fmt.Errorf("%w: model gateway budget gate: %w",
			queue.ErrDependencyDown, gerr)
	}
	// A spent cap delivers a refusal and consumes the batch.
	if msg != "" {
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, msg, "", topic, "", "budget-"+turnID)
		}
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
	rendered, observedMark := l.buildAgentPrompt(folder, topic, trigger)
	// Dropped once consumed, so a re-fed turn does not re-attach a stale reason.
	if v, ok := l.pendingReason.LoadAndDelete(turnID); ok {
		pr := v.(proactiveResult)
		rendered = proactiveReasonBlock(pr.check, pr.reason) + rendered
	}
	// The turn_context row is the failed prior attempt's (spec 5/12).
	if tc, ok := l.db.GetTurnContext(turnID); ok && tc.RetryCount > 0 {
		rendered = retryNoteBlock(tc.RetryCount, l.maxTurnRetry) + rendered
	}

	// forwarded_from is the turn's return address: reply/send/document deliver
	// to the origin chat, not the child folder JID the run addresses.
	live, err := l.db.PutTurnContext(turnID, folder, topic, chatJID, last.Sender, last.ForwardedFrom)
	if err != nil {
		return false, false, false, "", err
	}
	if !live {
		// A re-fed batch whose run is done: skip, so a sibling batch's steer
		// does not replay finished output.
		return false, false, false, "", nil
	}
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
	// batch re-feeds, the breaker streak is untouched.
	var gwKey turnKey
	if l.gateway != nil {
		k, gerr := l.gateway.mint(ctx, "arizuko-"+turnID+"-"+randHex(4),
			folder, turnID, turnCallerSub(last.Sender), l.runTimeout)
		if gerr != nil {
			slog.Error("model gateway refused to mint this turn's key; no spawn",
				"folder", folder, "turn_id", turnID, "err", gerr)
			return false, false, false, "", fmt.Errorf("%w: model gateway mint: %w",
				queue.ErrDependencyDown, gerr)
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
	// Type on the chat that RECEIVES the reply: the origin for a forwarded turn.
	typingJID := chatJID
	if last.ForwardedFrom != "" {
		typingJID = last.ForwardedFrom
	}
	l.beginProgress(typingJID)
	out, derr := l.dispatchRun(ctx, folder, topic, chatJID, turnID, last.Sender, rendered, elevated, gwKey.Key)
	if derr != nil {
		l.pauseProgress(typingJID, out.RunID)
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		if runedUnreachable(derr) {
			// Nothing ran: the queue's breaker leaves the folder's streak alone
			// (BUGS C9); the batch re-feeds next poll either way.
			derr = fmt.Errorf("%w: %w", queue.ErrDependencyDown, derr)
		}
		return false, false, false, "", derr
	}
	if out.Busy {
		l.pauseProgress(typingJID, out.RunID)
		// runed keeps no internal queue: leave turn_context running and the
		// cursor un-advanced, not counted toward the breaker.
		slog.Debug("dispatch run busy — runed at capacity, retry next poll", "folder", folder, "turn_id", turnID)
		return false, false, true, "", nil
	}
	if out.RunID != "" {
		_ = l.db.SetTurnRunID(turnID, out.RunID)
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
	if out.Outcome == runedv1.OutcomeError {
		// A turn that died without a reply retries up to maxTurnRetry. A Terminal
		// failure never retries: it cannot repair a misconfiguration (spec 5/12).
		tc, _ := l.db.GetTurnContext(turnID)
		hasBotReply := l.db.TurnHasBotReply(tc.ChatJID, turnID)
		if !out.Terminal && !hasBotReply && tc.RetryCount < l.maxTurnRetry {
			newCount, err := l.db.IncrementRetryCount(turnID)
			if err == nil {
				slog.Warn("turn failed without reply, scheduling retry",
					"folder", folder, "turn_id", turnID, "retry", newCount, "max", l.maxTurnRetry)
				// The re-feed IS the retry: busy=true holds the cursor on this batch.
				// Advancing first woke a poll with nothing left to retry (BUGS L75a).
				l.pauseProgress(typingJID, out.RunID)
				return false, false, true, "", nil
			}
		}
		// Final failure: retries exhausted, or a partial reply exists.
		slog.Warn("run outcome error", "folder", folder, "turn_id", turnID, "err", out.Error,
			"retry_count", tc.RetryCount)
		_ = l.db.MarkChatErrored(chatJID)
		_ = l.db.MarkMessagesErrored(trigger)
		if l.deliver != nil {
			notice := runFailureNotice
			if !hasBotReply && tc.RetryCount > 0 && tc.RetryCount >= l.maxTurnRetry {
				notice = retryExhaustedNotice(l.maxTurnRetry)
			}
			// A terminal failure names the real cause (spec 5/12). The agent's
			// OWN report is preferred: out.Error is the container's stderr
			// tail, truncated mid-word and carrying ant's log lines, so a user
			// was shown "ake says no" for an API Error: 400 (BUGS L349).
			if out.Terminal {
				cause := l.db.TurnFailure(folder, turnID)
				if cause == "" {
					cause = out.Error
				}
				notice = terminalFailureNotice(cause)
			}
			// Every chat this run owed an answer to: a follow-up steered into the
			// live container was marked done long before the run died (BUGS T2).
			// Only the primary carries the topic — a thread id is local to its
			// chat. The idempotency key is per destination, or the adapter dedups
			// the fan-out into one delivery.
			for _, jid := range l.db.chatsOwnedBy(chatJID, out.RunID) {
				thread := ""
				if jid == chatJID {
					thread = topic
				}
				_, _ = l.deliver.Send(jid, notice, "", thread, "", "fail-"+turnID+"-"+jid)
			}
		}
		finishTurn(l.db, l.deliver, turnID, endFailed)
		// The breaker rides only on the run that trips it.
		if out.BreakerOpen {
			l.onCircuitBreakerOpen(chatJID, folder)
		}
		return false, false, false, "", nil
	}
	end := endDone
	result, err := l.db.store().GetTurnResult(folder, turnID)
	if err != nil {
		return false, false, false, "", err
	}
	if result.Status == "error" {
		end = endFailed
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
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, silentTurnNotice, "", topic, "", "silent-"+turnID)
		}
	}
	obs.SetCircuitBreakerState(folder, 0) // 0=closed: a clean run clears the breaker
	// The errored flag is a claim about NOW; without this dual it is monotonic
	// for the life of the instance (BUGS L304).
	_ = l.db.ClearChatErrored(chatJID)
	finishTurn(l.db, l.deliver, turnID, end)
	return out.Outcome != runedv1.OutcomeSilent, false, false, observedMark, nil
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
	if err := l.db.ClearObservedCursor(folder, childTopic); err != nil {
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

// runFailureNotice is sent when a run returns outcome:error with no usable output.
const runFailureNotice = "Failed: agent error on that message. Try rephrasing or send a different message."

// retryExhaustedNotice is sent when every retry ended without a reply (spec
// 5/12). The count is the value that governs the retries, never a literal.
func retryExhaustedNotice(attempts int) string {
	return fmt.Sprintf("⚠️ Agent couldn't complete this request after %d attempts.", attempts)
}

// terminalFailureNotice carries runed's error text: a retry cannot repair a
// terminal failure, so the notice names the cause (spec 5/12, BUGS F73).
func terminalFailureNotice(cause string) string {
	return "⚠️ Agent run failed and a retry cannot fix it: " + cause
}

// ghostGroupNotice is sent when a chat routes to a folder with no group row.
const ghostGroupNotice = "⚠️ This chat routes to a group that isn't set up — an operator needs to register it."

// retryNoteBlock renders the system note injected on retry attempts.
func retryNoteBlock(attempt, max int) string {
	return fmt.Sprintf("<system-note>This is retry attempt %d of %d. "+
		"The previous attempt was killed before completing. "+
		"Be conservative with resource usage.</system-note>\n", attempt+1, max)
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
	// timed/split.go sends a plain "timed" for a group-context task, which does
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
func (l *Loop) dispatchRun(ctx context.Context, folder, topic, chatJID, turnID, trigger, batch string, elevated bool, gatewayKey string) (runedv1.RunOutcome, error) {
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
	model, containerCfg := l.db.GroupConfig(folder)
	if model == "" {
		model = l.defaultModel
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
	return l.runner.Run(ctx, runedv1.RunRequest{
		Folder:  types.Folder(folder),
		Topic:   topic,
		ChatJID: chatJID,
		// The JID scheme picks the per-surface output style; a scheme-less JID
		// yields "" → default. SplitN would hand back the folder path as a
		// style name, so jidScheme.
		Channel:       jidScheme(chatJID),
		SessionID:     sessionID,
		MessageBatch:  batch,
		TriggerSender: trigger,
		CallerSub:     caller,
		TurnID:        turnID,
		Model:         model,
		// Instance-wide; empty leaves the harness CLI's own default (high).
		Effort:          l.defaultEffort,
		ContainerConfig: containerCfg,
		Isolated:        isolated,
		Elevated:        elevated,
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
