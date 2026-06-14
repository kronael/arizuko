package routd

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
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
	r := l.resolve(chatJID, last)
	if !r.ok {
		if r.Observe != "" {
			ids := make([]string, len(msgs))
			for i := range msgs {
				ids[i] = msgs[i].ID
				if l.media.Enabled {
					l.enrichAttachments(context.Background(), &msgs[i], r.Observe)
				}
			}
			if err := l.db.MarkMessagesObserved(r.Observe, ids); err != nil {
				slog.Warn("process: mark observed", "jid", chatJID, "err", err)
			}
		}
		l.advance(chatJID, last)
		return false, nil
	}
	folder := r.Folder
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
	for _, batch := range groups {
		bl := batch[len(batch)-1]
		// A set sticky #topic overrides the message's own topic; a route-pinned
		// topic is the explicit non-web override.
		topic := l.effectiveTopic(chatJID, bl.Topic)
		if !strings.HasPrefix(chatJID, "web:") && r.Topic != "" {
			topic = r.Topic
		}
		had, steered, derr := l.runTurn(folder, topic, chatJID, bl.ID, batch)
		if derr != nil {
			// Transport failure: do NOT advance — re-fed next poll
			// (at-least-once; turn_results dedups). State stays running.
			return hadAny, derr
		}
		if steered {
			// Steer ack: the original run governs the batch; don't advance.
			return true, nil
		}
		hadAny = hadAny || had
	}
	l.advance(chatJID, last)
	return hadAny, nil
}

// runTurn dispatches ONE turn for a trigger batch (already bot-stripped),
// records its outcome, and returns (hadOutput, steered, err). It does NOT
// advance the agent_cursor — processGroupMessages advances once past the
// whole batch after all per-sender/per-topic turns close.
func (l *Loop) runTurn(folder, topic, chatJID, turnID string, trigger []core.Message) (bool, bool, error) {
	last := trigger[len(trigger)-1]
	// Pre-spawn budget gate. If today's folder spend hits the cap, deliver a
	// channel-visible refusal (no run dispatched) and consume the batch — return
	// hadOutput=true so processGroupMessages advances the cursor past it.
	if msg := l.budgetGate(folder, callerSubOfMsg(last.Sender)); msg != "" {
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, msg, "", topic, "", "budget-"+turnID)
		}
		return true, false, nil
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
	rendered := l.buildAgentPrompt(folder, topic, trigger)
	// A proactive turn carries one ephemeral <proactive_reason> block ahead of
	// the feed, dropped after it is consumed so a re-fed turn does not re-attach
	// a stale reason.
	if v, ok := l.pendingReason.LoadAndDelete(turnID); ok {
		pr := v.(proactiveResult)
		rendered = proactiveReasonBlock(pr.check, pr.reason) + rendered
	}
	// Inject retry note when this is a retry attempt (spec 5/40). The turn_context
	// row exists from the failed prior attempt; read its retry_count.
	if tc, ok := l.db.GetTurnContext(turnID); ok && tc.RetryCount > 0 {
		rendered = retryNoteBlock(tc.RetryCount, l.maxTurnRetry) + rendered
	}

	// A delegated trigger carries forwarded_from = the origin chat JID; persist
	// it as the turn's return address so reply/send/document deliver back to the
	// origin, not the child folder JID the run addresses.
	live, err := l.db.PutTurnContext(turnID, folder, topic, chatJID, last.Sender, last.ForwardedFrom)
	if err != nil {
		return false, false, err
	}
	if !live {
		// The turn already completed (a re-fed batch whose run is done). Skip
		// re-dispatch so a sibling batch's steer doesn't replay finished output;
		// report no-output/not-steered so the loop advances past it.
		return false, false, nil
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
				folder: folder, topic: topic, chatJID: chatJID, turnID: turnID, trigger: last.Sender,
			}, ipcDir); serr == nil {
				defer stop()
			} else {
				slog.Warn("serve turn mcp", "folder", folder, "turn_id", turnID, "err", serr)
			}
		}
	}

	// Write the per-spawn snapshots the in-container agent reads (current_tasks /
	// available_groups JSON) right before dispatch. Root sees all tasks + groups;
	// a child sees only its own tasks and no groups list. Skip when no ipc dir is
	// configured (REST/unit tests) so the write doesn't resolve to a relative
	// path under cwd.
	if l.folders != nil && l.folders.IpcDir != "" {
		isRoot := groupfolder.IsRoot(folder)
		container.WriteTasksSnapshot(l.folders, folder, isRoot, l.db.Tasks(folder, isRoot))
		container.WriteGroupsSnapshot(l.folders, folder, isRoot, slices.Collect(maps.Values(l.db.AllGroups())))
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
	// addresses (which usually has no presence channel). Matches gated's deliverTo.
	typingJID := chatJID
	if last.ForwardedFrom != "" {
		typingJID = last.ForwardedFrom
	}
	if l.deliver != nil {
		_ = l.deliver.Typing(typingJID, true)
	}
	out, derr := l.dispatchRun(folder, topic, chatJID, turnID, last.Sender, rendered)
	if l.deliver != nil {
		_ = l.deliver.Typing(typingJID, false)
	}
	if derr != nil {
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		return false, false, derr
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
		_ = l.db.SetTurnState(turnID, "done")
		_ = l.db.SetRunReturned(turnID)
		return true, true, nil
	}
	// POST /v1/runs has returned: close the callback surface so a late frame
	// 409s, even if an early submit_turn already flipped state→done.
	_ = l.db.SetRunReturned(turnID)
	if out.Outcome == runedv1.OutcomeError {
		// The run failed. Check if we can retry: a turn that died without
		// delivering a reply (SIGKILL/OOM/timeout) gets rescheduled up to
		// maxTurnRetry times before we give up and notify the user.
		tc, _ := l.db.GetTurnContext(turnID)
		hasBotReply := l.db.TurnHasBotReply(turnID)
		if !hasBotReply && tc.RetryCount < l.maxTurnRetry {
			newCount, err := l.db.IncrementRetryCount(turnID)
			if err == nil {
				slog.Warn("turn failed without reply, scheduling retry",
					"folder", folder, "turn_id", turnID, "retry", newCount, "max", l.maxTurnRetry)
				time.AfterFunc(retryBackoff, func() {
					_ = l.db.ResetTurnForRetry(turnID)
					l.Enqueue(chatJID)
				})
				return false, false, nil
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
				notice = retryExhaustedNotice
			}
			_, _ = l.deliver.Send(chatJID, notice, "", topic, "", "fail-"+turnID)
		}
		_ = l.db.SetTurnState(turnID, "done")
		// The breaker rides only on the run that trips it (runed reports
		// BreakerOpen on that run's outcome). When set, prune the chat's errored
		// rows + clear the folder session.
		if out.BreakerOpen {
			l.onCircuitBreakerOpen(chatJID, folder)
		}
		return false, false, nil
	}
	// Clean turn-boundary outcome: persist session_id backstop UNLESS submit_turn
	// already recorded one (its value wins).
	if out.SessionID != "" && !l.db.TurnResultRecorded(folder, turnID) {
		_ = l.db.PutSession(folder, topic, out.SessionID)
	}
	_ = l.db.SetTurnState(turnID, "done")
	return out.Outcome != runedv1.OutcomeSilent, false, nil
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
	l.copyParentSession(folder, parent, childUUID)
}

// copyParentSession looks up the parent topic's session_id and copies its
// Claude Code session jsonl to childUUID. No-op when the parent has no session
// yet (cold-start main: child gets a fresh session). Failures log WARN and
// proceed — the agent runs fine without forked context.
func (l *Loop) copyParentSession(folder, parent, childUUID string) {
	parentUUID, ok := l.db.GetSession(folder, parent)
	if !ok || parentUUID == "" {
		return
	}
	groupDir, err := l.folders.GroupPath(folder)
	if err != nil {
		slog.Warn("copyParentSession: group path", "folder", folder, "err", err)
		return
	}
	if err := container.CopySession(groupDir, parentUUID, childUUID); err != nil {
		slog.Warn("copyParentSession: cp failed",
			"folder", folder, "parent", parent,
			"parentUUID", parentUUID, "childUUID", childUUID, "err", err)
	}
}

// groupBySender splits msgs into consecutive same-sender runs, preserving
// causal order: A,B,A yields [A],[B],[A], not [A,A],[B]. Regrouping the whole
// slice by sender would reorder a conversation's turns.
func groupBySender(msgs []core.Message) [][]core.Message {
	if len(msgs) == 0 {
		return nil
	}
	var batches [][]core.Message
	for i, m := range msgs {
		if i == 0 || m.Sender != msgs[i-1].Sender {
			batches = append(batches, nil)
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], m)
	}
	return batches
}

// groupByTopic splits msgs into consecutive same-topic runs, preserving causal
// order: A,B,A yields [A],[B],[A], not [A,A],[B]. Regrouping the whole backlog
// by topic would reorder turns across topics (the web: per-topic dispatch).
func groupByTopic(msgs []core.Message) [][]core.Message {
	if len(msgs) == 0 {
		return nil
	}
	var batches [][]core.Message
	for i, m := range msgs {
		if i == 0 || m.Topic != msgs[i-1].Topic {
			batches = append(batches, nil)
		}
		batches[len(batches)-1] = append(batches[len(batches)-1], m)
	}
	return batches
}

// runFailureNotice is sent to the chat when a run returns outcome:error and
// produced no usable output, so the user isn't left silent.
const runFailureNotice = "Failed: agent error on that message. Try rephrasing or send a different message."

// retryExhaustedNotice is sent when all retry attempts are exhausted without
// a reply (spec 5/40 turn-retry).
const retryExhaustedNotice = "⚠️ Agent couldn't complete this request after 3 attempts."

// retryBackoff is the delay between retry attempts (spec 5/40).
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
func (l *Loop) dispatchRun(folder, topic, chatJID, turnID, trigger, batch string) (runedv1.RunOutcome, error) {
	ctx := context.Background()
	var cancel context.CancelFunc
	if l.runTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, l.runTimeout)
		defer cancel()
	}
	caller := types.UserSub("service:routd")
	if trigger != "" && !strings.HasPrefix(trigger, "timed-") && !strings.HasPrefix(trigger, "system") {
		caller = types.UserSub(trigger)
	}
	model, containerCfg := l.db.GroupConfig(folder)
	if model == "" {
		model = l.defaultModel
	}
	// Resolve the per-folder grant rules + egress allowlist HERE (routd is the
	// authz plane; runed has neither store) and ship them so runed can attach the
	// spawn to the crackbox network + honor share_mount. nil allowlist on error
	// is fine — runed treats empty as no-extra-constraint.
	allowlist, _ := l.db.ResolveAllowlist(folder)
	return l.runner.Run(ctx, runedv1.RunRequest{
		Folder:           types.Folder(folder),
		Topic:            topic,
		ChatJID:          chatJID,
		// Channel = the JID scheme (telegram|slack|discord|web) so the container
		// picks the per-surface output style. gated derived this; the split dropped
		// it → every channel got default formatting (markdown unrendered on
		// Telegram, etc.). A bare-folder/operator/timed JID (no scheme) yields ""
		// → default style; SplitN would wrongly return the whole folder path as a
		// bogus style name, so use jidScheme.
		Channel:          jidScheme(chatJID),
		SessionID:        l.db.SessionID(folder, topic),
		MessageBatch:     batch,
		TriggerSender:    trigger,
		CallerSub:        caller,
		TurnID:           turnID,
		CapabilityScopes: l.scopes,
		Model:            model,
		ContainerConfig:  containerCfg,
		Isolated:         strings.HasPrefix(trigger, "timed-isolated:"),
		Grants:           deriveFolderGrants(l.db, folder),
		EgressAllowlist:  allowlist,
	})
}
