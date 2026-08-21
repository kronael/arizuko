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

// processGroupMessages is the queue's per-folder-serialized worker. It
// renders the batch and dispatches one run to runed, recording the
// outcome. Returns (hadOutput, err) per the queue's processMessagesFn.
func (l *Loop) processGroupMessages(chatJID string) (bool, error) {
	cursor := l.db.GetAgentCursor(chatJID)
	msgs, err := l.db.MessagesSince(chatJID, cursor)
	if err != nil {
		return false, err
	}
	if len(msgs) == 0 {
		return false, nil
	}
	last := msgs[len(msgs)-1]
	// A batch runed refused as Busy retries on a growing backoff, not on
	// every 2s poll: the re-feed below this line is the expensive part
	// (capability resolve, MCP socket, prompt build). A new inbound message
	// resets the backoff inside busyDeferred, so steering and fresh user
	// turns are never delayed. Keyed on the true newest id — a bot row is
	// still new activity worth resetting on.
	if l.busyDeferred(chatJID, last.ID) {
		return false, nil
	}
	// Route and steer both read the newest USER row, never the newest row.
	// A `verb=mention` route matches on the message's verb, and a bot reply
	// carries no mention — so resolving on a bot row turns the user's @mention
	// into a route miss and archives it as context, silently, on the shipped
	// mention-only Slack/Discord config. The steer half of the same mistake was
	// BUGS L56; this is its twin one line up. An all-bot batch routes nothing.
	steerMsg, hasUser := newestUserMsg(msgs)
	if !hasUser {
		l.advance(chatJID, last)
		return false, nil
	}
	r := l.resolve(chatJID, steerMsg)
	if !r.ok {
		l.routeMiss(chatJID, msgs, r)
		return false, nil
	}
	folder := r.Folder
	// Steering layer (sticky-nav / slash commands / @child delegation) consumes
	// the latest message BEFORE it reaches a turn. It MUST run here, on the one
	// path every trigger converges to: adapter ingest enqueues straight into this
	// worker (server.go handleInbound), so when steering lived only in the
	// pollOnce backstop it raced the queue and lost — a Slack "/root mint …"
	// reached the agent as plain unelevated text and the elevation never fired
	// (marinade atlas, 2026-07-16). A consumed message advances the cursor and
	// never spawns; cmdRoot re-injects its arg, which the next worker pass runs
	// elevated.
	// Steer the newest USER message, not the newest message. A slash command is
	// consumed here and nowhere else, and handleCommand refuses a bot row — so
	// when the agent's own reply lands after the command and before this poll,
	// steer inspects the reply, refuses it, and the command falls through to the
	// turn as plain text. anteval's `/new` was swallowed exactly that way twice
	// on krons (BUGS L56); the same hole loses a user's /stop or /root in any
	// chat busy enough to answer before the next poll. Advance past the steered
	// row only: anything newer has not been consumed.
	if l.steer(chatJID, steerMsg, folder) {
		l.advance(chatJID, steerMsg)
		return true, nil
	}
	// Strip bot rows from the trigger batch (don't feed the agent its own
	// output) but keep them in the rendered context.
	var trigger []core.Message
	for _, m := range msgs {
		if !m.BotMsg {
			trigger = append(trigger, m)
		}
	}
	if len(trigger) == 0 {
		l.advance(chatJID, last)
		return false, nil
	}

	// web: chats dispatch one turn per topic in first-seen order; everyone
	// else batches per distinct sender, one turn each. The route-pinned topic
	// (r.Topic) overrides the message's own topic for the non-web path.
	var groups [][]core.Message
	if strings.HasPrefix(chatJID, "web:") {
		groups = groupByTopic(trigger)
	} else {
		groups = groupBySender(trigger)
	}

	hadAny := false
	// Pending per-topic observed watermarks, applied only if this dispatch
	// reaches the advance below (BUGS L58).
	var marks []observedAdvance
	for _, batch := range groups {
		bl := batch[len(batch)-1]
		// A set sticky #topic overrides the message's own topic; a route-pinned
		// topic is the explicit non-web override.
		topic := l.effectiveTopic(chatJID, bl.Topic)
		if !strings.HasPrefix(chatJID, "web:") && r.Topic != "" {
			topic = r.Topic
		}
		had, steered, busy, mark, derr := l.runTurn(folder, topic, chatJID, bl.ID, batch)
		if derr != nil {
			// Transport failure: do NOT advance — re-fed next poll
			// (at-least-once; turn_results dedups). State stays running.
			return hadAny, derr
		}
		if busy {
			// runed didn't admit the run (folder busy with a dead container, or
			// the global cap is hit) and keeps no internal queue. Busy is NOT an
			// error (distinct from the route-miss above, which is a resolve
			// failure): do NOT advance the cursor and do NOT return an err (which
			// would trip the queue breaker). The batch is re-fed after a growing
			// per-chat backoff (noteBusy) — turn_context stays 'running' so the
			// re-dispatch is live.
			l.noteBusy(chatJID, last.ID)
			return hadAny, nil
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
	// The observed watermark rides the SAME success path as the message cursor
	// (BUGS L58). Every early return above re-feeds the batch, and a re-fed turn
	// must still see the ambient context the failed attempt was shown.
	for _, m := range marks {
		if err := l.db.UpdateObservedCursor(folder, m.topic, m.ts); err != nil {
			slog.Error("observed cursor advance failed — the topic re-sees this window next turn",
				"folder", folder, "topic", m.topic, "ts", m.ts, "err", err)
		}
	}
	return hadAny, nil
}

// observedAdvance is one topic's pending observed watermark for this dispatch.
type observedAdvance struct{ topic, ts string }

// newestUserMsg returns the newest non-bot message in a batch. ok=false when the
// batch is all bot rows, which nothing may steer on.
func newestUserMsg(msgs []core.Message) (core.Message, bool) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if !msgs[i].BotMsg {
			return msgs[i], true
		}
	}
	return core.Message{}, false
}

// Busy re-dispatch backoff: base delay for the first refused attempt,
// doubling per consecutive refusal of the same batch, capped. At the cap a
// starved chat probes runed once a minute instead of once per 2s poll — a
// restart's migrate wave on krons burned 4,704 dispatches (each with a full
// MCP-socket + prompt setup) for 18 spawns in 15 minutes (BUGS L55).
const (
	busyBackoffBase = 4 * time.Second
	busyBackoffCap  = 60 * time.Second
)

// busyState is one chat's re-dispatch backoff after a runed Busy refusal.
type busyState struct {
	// until defers the next dispatch attempt.
	until time.Time
	// delay is the backoff applied at the last refusal; the next one doubles it.
	delay time.Duration
	// lastMsgID is the chat's newest message id at refusal time. A different
	// newest id means fresh inbound activity: the backoff no longer applies.
	lastMsgID string
}

// busyDeferred reports whether the chat's batch is inside its busy backoff
// window and should not be re-dispatched this poll. A changed newest-message
// id clears the backoff (new activity retries immediately); an expired window
// lets the attempt through and keeps the entry so a repeat refusal doubles.
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

// noteBusy records a runed Busy refusal for the chat's current batch,
// doubling the backoff on a consecutive refusal of the same batch.
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

// runTurn dispatches ONE turn for a trigger batch (already bot-stripped),
// records its outcome, and returns (hadOutput, steered, busy, err). It does
// NOT advance the agent_cursor — processGroupMessages advances once past the
// whole batch after all per-sender/per-topic turns close. busy=true means
// runed rejected admission (at capacity); the caller must re-feed, not advance.
func (l *Loop) runTurn(folder, topic, chatJID, turnID string, trigger []core.Message) (hadOutput, steered, busy bool, observedMark string, rerr error) {
	last := trigger[len(trigger)-1]
	// Elevation: a message cmdRoot registered in pendingElevation (operator-gated,
	// unforgeable) raises THIS turn to root. One-shot — consumed here so a re-fed
	// batch never re-elevates. Scan the whole batch (not just last) so batching
	// with a sibling can't drop the signal.
	elevated := false
	for i := range trigger {
		if _, ok := l.pendingElevation.LoadAndDelete(trigger[i].ID); ok {
			elevated = true
		}
	}
	// turn span + duration/count metric (spec 5/O). Trace ID derives from the
	// turn ID so every daemon handling this turn shares one trace; the span ctx
	// flows into dispatchRun so container_spawn/cross_daemon nest under it.
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
	// Ghost-group guard. A route/engagement/sticky rule can point at a folder
	// with no registered group row — hand-created, or one whose row was deleted
	// and whose route cascade failed (DeleteGroup logs a cascade error rather than
	// failing, and names this guard as the backstop). Its home may be mis-owned, so
	// the agent spawns config-less and silently never replies. Refuse loud + tell the chat,
	// and consume the batch (advance the cursor, no poison replay). Observe/web/
	// hook targets never reach here — resolve() handles them before runTurn.
	if !l.db.GroupExists(folder) {
		slog.Error("dispatch refused: unregistered group", "folder", folder, "jid", chatJID, "turn_id", turnID)
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, ghostGroupNotice, "", topic, "", "ghost-"+turnID)
		}
		return true, false, false, "", nil
	}
	// Pre-spawn budget gate. If today's folder spend hits the cap, deliver a
	// channel-visible refusal (no run dispatched) and consume the batch — return
	// hadOutput=true so processGroupMessages advances the cursor past it.
	if msg := l.budgetGate(folder, callerSubOfMsg(last.Sender)); msg != "" {
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, msg, "", topic, "", "budget-"+turnID)
		}
		return true, false, false, "", nil
	}
	// Enqueue new_day / new_session system messages BEFORE building the prompt
	// so buildAgentPrompt's FlushSysMsgs renders them this turn.
	l.emitSystemEvents(folder, chatJID)
	// The first turn in a previously-unseen topic auto-forks lineage from the
	// topic of the message it replies to, falling back to "" (main). Copies the
	// parent topic's Claude session so the child resumes from its tail.
	parent := ""
	if last.ReplyToID != "" {
		parent = l.db.TopicByID(last.ReplyToID)
	}
	l.ensureTopicWithFork(folder, topic, parent)
	// Download inbound media + transcribe voice/video into the trigger before the
	// prompt is built. Rewrites each row's content + persists via EnrichMessage so
	// later turns' observed context sees the transcript too. No-op when
	// MEDIA_ENABLED is off.
	if l.media.Enabled {
		ctx := context.Background()
		for i := range trigger {
			l.enrichAttachments(ctx, &trigger[i], folder)
		}
	}
	rendered, observedMark := l.buildAgentPrompt(folder, topic, trigger)
	// A proactive turn carries one ephemeral <proactive_reason> block ahead of
	// the feed, dropped after it is consumed so a re-fed turn does not re-attach
	// a stale reason.
	if v, ok := l.pendingReason.LoadAndDelete(turnID); ok {
		pr := v.(proactiveResult)
		rendered = proactiveReasonBlock(pr.check, pr.reason) + rendered
	}
	// Inject retry note when this is a retry attempt (spec 5/12). The turn_context
	// row exists from the failed prior attempt; read its retry_count.
	if tc, ok := l.db.GetTurnContext(turnID); ok && tc.RetryCount > 0 {
		rendered = retryNoteBlock(tc.RetryCount, l.maxTurnRetry) + rendered
	}

	// A delegated trigger carries forwarded_from = the origin chat JID; persist
	// it as the turn's return address so reply/send/document deliver back to the
	// origin, not the child folder JID the run addresses.
	live, err := l.db.PutTurnContext(turnID, folder, topic, chatJID, last.Sender, last.ForwardedFrom)
	if err != nil {
		return false, false, false, "", err
	}
	if !live {
		// The turn already completed (a re-fed batch whose run is done). Skip
		// re-dispatch so a sibling batch's steer doesn't replay finished output;
		// report no-output/not-steered so the loop advances past it.
		return false, false, false, "", nil
	}
	// Record the trigger message id so a threaded reply can root a new
	// platform thread on it (replyThreadRoot).
	_ = l.db.SetTurnTriggerMsg(turnID, last.ID)
	_ = l.db.SetLastReply(chatJID, topic, last.ID, folder)

	// Stand up the per-turn agent MCP socket in-process; stop() removes it when
	// runTurn returns (dispatchRun is synchronous, so the socket lives for the
	// whole run). routd and runed agree on the path via folders.IpcPath.
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

	// Write the per-spawn snapshots the in-container agent reads (current_tasks /
	// available_groups JSON) right before dispatch. The all-groups/all-tasks view
	// is a root privilege, so it tracks elevation — NOT folder shape (a top-level
	// tenant is not root). A non-elevated turn sees only its own tasks and no
	// groups list. Skip when no ipc dir is configured (REST/unit tests) so the
	// write doesn't resolve to a relative path under cwd.
	if l.folders != nil && l.folders.IpcDir != "" {
		container.WriteTasksSnapshot(l.folders, folder, elevated, l.db.Tasks(folder, elevated))
		container.WriteGroupsSnapshot(l.folders, folder, elevated, slices.Collect(maps.Values(l.db.AllGroups())))
	}

	// Reset a long-idle session before the spawn reads it: a folder whose chat
	// cursor is older than the idle threshold resumes from a stale Claude session
	// the agent has long forgotten, so start fresh instead. Isolated (timed) runs
	// carry no session lineage, so skip them.
	if !strings.HasPrefix(last.Sender, "timed-isolated:") {
		if sid := l.db.SessionID(folder, topic); sid != "" && l.sessionIdleExpired(chatJID) {
			slog.Info("session: resetting on idle expiry",
				"folder", folder, "topic", topic, "jid", chatJID, "threshold", l.sessionIdle)
			_ = l.db.DeleteSession(folder, topic)
		}
	}

	slog.Info("dispatch run", "folder", folder, "topic", topic, "turn_id", turnID,
		"chat_jid", chatJID, "trigger", last.Sender)
	// Type on the chat that RECEIVES the reply: for a delegated/forwarded turn
	// that's the origin (ForwardedFrom), not the child folder JID the run
	// addresses (which usually has no presence channel).
	typingJID := chatJID
	if last.ForwardedFrom != "" {
		typingJID = last.ForwardedFrom
	}
	if l.deliver != nil {
		_ = l.deliver.Typing(typingJID, true)
	}
	out, derr := l.dispatchRun(ctx, folder, topic, chatJID, turnID, last.Sender, rendered, elevated)
	// For steered turns (container already running, returned immediately), the
	// container is still processing — don't clear typing here. appendAndDeliver
	// clears it when content lands. For all other outcomes (including errors and
	// silent runs), clear now — and clear it for EVERY chat this run owns, not
	// just this turn's. A turn steered into this run handed its indicator to a
	// submit_turn that never arrives when the run dies at the timeout wall, and
	// steering is by FOLDER, so that turn's chat is not always this one. Its
	// indicator then ran to the adapter backstop, which is the twenty-minute
	// "typing…" a user reported (BUGS T1).
	if l.deliver != nil && !out.Steered {
		for _, jid := range l.typingOwnedBy(typingJID, out.RunID) {
			_ = l.deliver.Typing(jid, false)
		}
	}
	if derr != nil {
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		if runedUnreachable(derr) {
			// runed never answered, so nothing ran and this folder failed at
			// nothing. Tag it so the queue's breaker leaves the group's streak
			// alone (BUGS C9); the batch re-feeds next poll either way.
			derr = fmt.Errorf("%w: %w", queue.ErrDependencyDown, derr)
		}
		return false, false, false, "", derr
	}
	if out.Busy {
		// runed rejected admission (folder busy with a dead container, or the
		// global concurrency cap is hit) and keeps no internal queue. This is
		// neither an error nor a run: leave turn_context 'running' (no
		// SetRunReturned, no SetTurnState) and the cursor un-advanced so the poll
		// loop re-dispatches this turn — bounded by the poll interval, and NOT
		// counted toward the breaker. Typing was already cleared above (the
		// non-steered branch) since no container spawned; the next attempt re-flips
		// it.
		slog.Debug("dispatch run busy — runed at capacity, retry next poll", "folder", folder, "turn_id", turnID)
		return false, false, true, "", nil
	}
	// Persist the runed-assigned run_id for reconciliation (turn_context.run_id).
	if out.RunID != "" {
		_ = l.db.SetTurnRunID(turnID, out.RunID)
	}
	if out.Steered {
		// The batch was written into the folder's already-live container; the
		// original run owns it and will submit_turn under ITS turn_id. Mark THIS
		// turn_context done so a re-fed poll (or recoverPending after a restart)
		// sees it terminal and skips re-dispatch — else it re-runs as a fresh
		// spawn and duplicates the output.
		if err := l.db.SetTurnState(turnID, "done"); err != nil {
			slog.Error("processGroupMessages: SetTurnState(done) failed on steer; turn may appear stuck and be re-dispatched, duplicating output",
				"turn_id", turnID, "err", err)
		}
		_ = l.db.SetRunReturned(turnID)
		return true, true, false, "", nil
	}
	// POST /v1/runs has returned: close the callback surface so a late frame
	// 409s, even if an early submit_turn already flipped state→done.
	_ = l.db.SetRunReturned(turnID)
	if out.Outcome == runedv1.OutcomeError {
		// The run failed. Check if we can retry: a turn that died without
		// delivering a reply (SIGKILL/OOM/timeout) gets rescheduled up to
		// maxTurnRetry times before we give up and notify the user. A failure
		// runed marked Terminal is never retried — a retry cannot repair a
		// misconfiguration and only burns spawns (spec 5/12).
		tc, _ := l.db.GetTurnContext(turnID)
		hasBotReply := l.db.TurnHasBotReply(turnID)
		if !out.Terminal && !hasBotReply && tc.RetryCount < l.maxTurnRetry {
			newCount, err := l.db.IncrementRetryCount(turnID)
			if err == nil {
				slog.Warn("turn failed without reply, scheduling retry",
					"folder", folder, "turn_id", turnID, "retry", newCount, "max", l.maxTurnRetry)
				time.AfterFunc(retryBackoff, func() {
					_ = l.db.ResetTurnForRetry(turnID)
					l.Enqueue(chatJID)
				})
				return false, false, false, "", nil
			}
		}
		// Final failure: all retries exhausted or agent did reply (partial is OK).
		slog.Warn("run outcome error", "folder", folder, "turn_id", turnID, "err", out.Error,
			"retry_count", tc.RetryCount)
		_ = l.db.MarkChatErrored(chatJID)
		ids := make([]string, len(trigger))
		for i, m := range trigger {
			ids[i] = m.ID
		}
		_ = l.db.MarkMessagesErrored(ids)
		if l.deliver != nil {
			notice := runFailureNotice
			// Use retry-exhausted notice only when retries were actually attempted.
			if !hasBotReply && tc.RetryCount > 0 && tc.RetryCount >= l.maxTurnRetry {
				notice = retryExhaustedNotice(l.maxTurnRetry)
			}
			// A terminal failure names the real cause: the generic text
			// hides a misconfiguration an operator must fix (spec 5/12).
			if out.Terminal {
				notice = terminalFailureNotice(out.Error)
			}
			_, _ = l.deliver.Send(chatJID, notice, "", topic, "", "fail-"+turnID)
		}
		if err := l.db.SetTurnState(turnID, "done"); err != nil {
			slog.Error("processGroupMessages: SetTurnState(done) failed on terminal failure; turn may appear stuck in-progress",
				"turn_id", turnID, "err", err)
		}
		// The breaker rides only on the run that trips it (runed reports
		// BreakerOpen on that run's outcome). When set, prune the chat's errored
		// rows + clear the folder session.
		if out.BreakerOpen {
			l.onCircuitBreakerOpen(chatJID, folder)
		}
		return false, false, false, "", nil
	}
	// Clean turn-boundary outcome.
	recorded := l.db.TurnResultRecorded(folder, turnID)
	l.persistSessionBackstop(folder, topic, turnID, last.Sender, out, recorded)
	// Silent turn: a clean run that recorded neither a result nor a bot reply
	// delivered nothing — the user saw typing then silence. Deliberate silence
	// still records a result (ant calls deliverTurn on every result event,
	// think-only included), so this fires only on a real malfunction: a 0-result
	// SDK run, a config-less spawn, a swallowed submit_turn. Not transient — never
	// retry; surface it loud AND to the chat so it can never be invisible again.
	if out.Outcome == runedv1.OutcomeOK && !recorded && !l.db.TurnHasBotReply(turnID) {
		slog.Error("silent turn: clean run delivered nothing",
			"folder", folder, "turn_id", turnID, "jid", chatJID)
		obs.RecordSilentTurn(folder)
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, silentTurnNotice, "", topic, "", "silent-"+turnID)
		}
	}
	obs.SetCircuitBreakerState(folder, 0) // 0=closed: a clean run clears the breaker
	if err := l.db.SetTurnState(turnID, "done"); err != nil {
		slog.Error("processGroupMessages: SetTurnState(done) failed on clean completion; turn may appear stuck in-progress",
			"folder", folder, "turn_id", turnID, "err", err)
	}
	return out.Outcome != runedv1.OutcomeSilent, false, false, observedMark, nil
}

// ensureTopicWithFork ensures (folder, topic) has a sessions row and, when
// newly inserted, copies the parent topic's Claude Code session file so the
// child resumes from the parent's tail. Failures are logged but never block the
// turn — the child simply starts fresh. parent="" forks from main.
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

// inheritParentSession gives a freshly-forked topic its context. It copies the
// parent's Claude Code transcript under childUUID and, when it cannot, clears
// the child's observed cursor.
//
// Both halves are one rule: a fork stamps observed_cursor=now so the child reads
// only post-fork context, which is right ONLY while the transcript carries the
// rest. With no transcript that stamp leaves the child blind on both axes — the
// first turn of every new thread answering with no history at all (BUGS L54).
// Every fork path goes through here so the rule cannot drift.
func (l *Loop) inheritParentSession(folder, parent, childTopic, childUUID string) {
	if l.copyParentSession(folder, parent, childUUID) {
		return
	}
	if err := l.db.ClearObservedCursor(folder, childTopic); err != nil {
		slog.Warn("inheritParentSession: clearing the observed cursor failed — the child topic starts with no context",
			"folder", folder, "topic", childTopic, "err", err)
	}
}

// copyParentSession looks up the parent topic's session_id and copies its
// Claude Code session jsonl to childUUID. Reports whether the child now owns a
// resumable transcript: false when the parent has no session yet (cold-start
// main) or the copy failed. Failures log WARN and proceed — inheritParentSession
// restores the child's observed window instead.
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

// persistSessionBackstop records the session runed reported as the topic's
// session, so the next turn resumes it. Two cases skip the write: submit_turn
// already recorded one (the agent's value wins), and an isolated run — which
// resumes nothing and so becomes nothing. runed mints a throwaway session for an
// isolated spawn, and installing that would hand the chat the transcript a cron
// ran in (BUGS L54). recordTurnResult applies the same rule on its own path.
func (l *Loop) persistSessionBackstop(
	folder, topic, turnID, trigger string, out runedv1.RunOutcome, recorded bool,
) {
	if out.SessionID == "" || recorded || strings.HasPrefix(trigger, "timed-isolated:") {
		return
	}
	if err := l.db.PutSession(folder, topic, out.SessionID); err != nil {
		slog.Error("persistSessionBackstop: PutSession failed; the next turn in this topic loses session continuity",
			"folder", folder, "topic", topic, "turn_id", turnID, "err", err)
	}
}

// consecutiveRuns splits msgs into consecutive same-key runs, preserving causal
// order: A,B,A yields [A],[B],[A], not [A,A],[B]. Regrouping the whole slice by
// key would reorder a conversation's turns — across senders for a group chat,
// across topics for the web: per-topic dispatch.
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

func groupBySender(msgs []core.Message) [][]core.Message {
	return consecutiveRuns(msgs, func(m core.Message) string { return m.Sender })
}

func groupByTopic(msgs []core.Message) [][]core.Message {
	return consecutiveRuns(msgs, func(m core.Message) string { return m.Topic })
}

// typingOwnedBy lists the chats whose indicator a finished run must clear:
// primary (this turn's, resolved before the run so it holds even when the run
// reported no id) plus every chat of a turn recorded against runID. Duplicates
// are dropped so an adapter is not told twice.
func (l *Loop) typingOwnedBy(primary, runID string) []string {
	jids := []string{primary}
	if runID == "" {
		return jids
	}
	for _, jid := range l.db.TypingJIDsForRun(runID) {
		if jid != "" && !slices.Contains(jids, jid) {
			jids = append(jids, jid)
		}
	}
	return jids
}

// runFailureNotice is sent to the chat when a run returns outcome:error and
// produced no usable output, so the user isn't left silent.
const runFailureNotice = "Failed: agent error on that message. Try rephrasing or send a different message."

// retryExhaustedNotice is sent when all retry attempts are exhausted without a
// reply (spec 5/12 turn-retry). The count comes from the SAME value that
// governs the retries (MAX_TURN_RETRY); it was a literal "3" and told an
// operator running MAX_TURN_RETRY=2 that the agent had tried three times.
func retryExhaustedNotice(attempts int) string {
	return fmt.Sprintf("⚠️ Agent couldn't complete this request after %d attempts.", attempts)
}

// terminalFailureNotice is sent when runed marks a failure terminal
// (RunOutcome.Terminal): a retry cannot repair it, so the notice carries
// runed's error text and names the real cause — for example a bad agent
// image — instead of the generic failure text (spec 5/12, BUGS F73).
func terminalFailureNotice(cause string) string {
	return "⚠️ Agent run failed and a retry cannot fix it: " + cause
}

// silentTurnNotice is sent when a clean run delivered nothing — no turn result,
// no bot reply. A malfunction (config-less spawn, 0-result run), not deliberate
// silence (which records a result). See runTurn's clean-outcome epilogue.
const silentTurnNotice = "⚠️ I couldn't produce a reply to that — please re-send."

// ghostGroupNotice is sent when a chat routes to a folder with no registered
// group row (a ghost route). See runTurn's ghost-group guard.
const ghostGroupNotice = "⚠️ This chat routes to a group that isn't set up — an operator needs to register it."

// retryBackoff is the delay between retry attempts (spec 5/12).
const retryBackoff = 10 * time.Second

// retryNoteBlock renders the system note injected on retry attempts.
func retryNoteBlock(attempt, max int) string {
	return fmt.Sprintf("<system-note>This is retry attempt %d of %d. "+
		"The previous attempt was killed before completing. "+
		"Be conservative with resource usage.</system-note>\n", attempt+1, max)
}

// jidScheme returns the JID scheme (telegram|slack|discord|web) — the
// per-surface output-style selector — or "" for a bare folder / scheme-less JID
// (operator/timed/auto-migrate), which must map to the default style.
func jidScheme(jid string) string {
	if s, _, ok := strings.Cut(jid, ":"); ok {
		return s
	}
	return ""
}

// dispatchRun renders the run request and calls runed POST /v1/runs. The
// agent's conversation frames arrive out-of-band during the run via the
// /v1/turns/{turn_id}/* callbacks; this returns the turn-boundary outcome.
// turnCallerSub renders "who is this turn acting on behalf of" from its
// trigger — the ONE rule, so spawn-time secret resolution and the agent's
// per-tool-call connector-secret resolution cannot diverge. A timed or system
// trigger has no human behind it and resolves to routd itself.
//
// This is deliberately NOT the tool-call authorization principal: that asks
// "what may be done HERE", answered by folder:<path>. Whose credentials to
// inject and what is permitted in a place are different questions (BUGS B2).
func turnCallerSub(trigger string) string {
	if trigger == "" || strings.HasPrefix(trigger, "timed-") || strings.HasPrefix(trigger, "system") {
		return "service:routd"
	}
	return trigger
}

// runedUnreachable reports whether err means routd never reached runed at all:
// the connect phase failed, so no request was served and no run happened. That
// is compose boot order or a runed restart, not a verdict on the folder.
//
// A *runedv1.APIError is the opposite case and is excluded explicitly — runed
// took the request and answered with a refusal, which IS attributable. The
// client already draws that line ("a transport failure surfaces as the bare
// network error"); this reads it rather than inventing a second rule.
//
// Deliberately narrow. A mid-run context deadline also surfaces as a bare
// error, but the run DID start, so it stays a real failure and keeps counting.
func runedUnreachable(err error) bool {
	var apiErr *runedv1.APIError
	if errors.As(err, &apiErr) {
		return false
	}
	// The service name has no A record yet (compose brings runed up after routd).
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	// The name resolves but nothing is listening on :8080 yet.
	return errors.Is(err, syscall.ECONNREFUSED)
}

func (l *Loop) dispatchRun(ctx context.Context, folder, topic, chatJID, turnID, trigger, batch string, elevated bool) (runedv1.RunOutcome, error) {
	var cancel context.CancelFunc
	if l.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.runTimeout)
		defer cancel()
	}
	caller := types.UserSub(turnCallerSub(trigger))
	// An isolated scheduled task runs with NO `--resume` (specs/4/8
	// §"Implemented beyond base spec"). Passing the group's session id anyway
	// resumed the chat transcript and then appended the task's whole tool-heavy
	// run to it, so a folder's crons wrote themselves into the conversation the
	// people in that chat were having, and Claude Code compacted the humans out
	// first. sloth's #trading transcript was 8 cron turns to 4 human ones.
	isolated := strings.HasPrefix(trigger, "timed-isolated:")
	sessionID := ""
	if !isolated {
		sessionID = l.db.SessionID(folder, topic)
	}
	model, containerCfg := l.db.GroupConfig(folder)
	if model == "" {
		model = l.defaultModel
	}
	// Resolve the per-folder grant rules + egress allowlist HERE (routd is the
	// authz plane; runed has neither store) and ship them so runed can attach the
	// spawn to its own egress network + honor share_mount. nil allowlist on error
	// is fine — runed treats empty as no-extra-constraint.
	allowlist, _ := l.db.ResolveAllowlist(folder)
	// Container env carries the trigger user's OWN model credentials (BYOA) and
	// nothing else from `secrets`: a web-chat user's ANTHROPIC_API_KEY shadows the
	// operator anchor for their spawn. Folder capability credentials do NOT ride
	// along — they reach a tool only through the MCP broker, per call, narrowed to
	// the connector's declared secrets= list (spec 5/13 §Trust model). routd
	// decrypts here (it holds SECRETS_KEY); runed injects as container env.
	secrets := l.db.EnvProfileSecrets(string(caller))
	// 5/33: mounts/egress/web are GRANTS, not tier. routd (the authz plane) resolves
	// the three container-capability booleans from the folder's acl rows and ships
	// them typed so runed/container consume a decision, not a rule bundle. An elevated
	// /root turn holds `*` → all capabilities; a normal folder gets exactly what its
	// grants say (role:member — the messaging floor — grants none, so a plain folder
	// runs with a writable share, constrained egress, and no web surface until an
	// operator delegates egress/web:publish).
	sub := "folder:" + folder
	shareRO := !elevated && l.db.Authorize(sub, folder, "mcp:share_mount", map[string]string{"readonly": "true"})
	egress := elevated || l.db.Authorize(sub, folder, "egress", nil)
	webPublish := elevated || l.db.Authorize(sub, folder, "web:publish", nil)
	// A denied capability is otherwise unattributable: web:publish=false makes
	// container/runner skip both web bind mounts, so the agent finds no
	// ~/public_html and reports a broken mount. An agent in rhias/nemo spent a
	// session on that misdiagnosis and filed it four times because the host had
	// nothing to grep. This line IS the signal — one per spawn, with the folder
	// and every decision, so an absent surface points at its missing grant.
	slog.Info("dispatch capabilities resolved",
		"folder", folder, "turn_id", turnID, "elevated", elevated,
		"egress", egress, "share_readonly", shareRO, "web_publish", webPublish)
	return l.runner.Run(ctx, runedv1.RunRequest{
		Folder:  types.Folder(folder),
		Topic:   topic,
		ChatJID: chatJID,
		// Channel = the JID scheme (telegram|slack|discord|web) so the container
		// picks the per-surface output style. gated derived this; the split dropped
		// it → every channel got default formatting (markdown unrendered on
		// Telegram, etc.). A bare-folder/operator/timed JID (no scheme) yields ""
		// → default style; SplitN would wrongly return the whole folder path as a
		// bogus style name, so use jidScheme.
		Channel:         jidScheme(chatJID),
		SessionID:       sessionID,
		MessageBatch:    batch,
		TriggerSender:   trigger,
		CallerSub:       caller,
		TurnID:          turnID,
		Model:           model,
		ContainerConfig: containerCfg,
		Isolated:        isolated,
		Elevated:        elevated,
		ShareReadOnly:   shareRO,
		Egress:          egress,
		WebPublish:      webPublish,
		EgressAllowlist: allowlist,
		Secrets:         secrets,
	})
}
