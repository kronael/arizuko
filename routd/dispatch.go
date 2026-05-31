package routd

// dispatch.go holds the per-turn dispatch path: the queue's per-folder worker
// (processGroupMessages), the single-turn driver (runTurn), the runed POST
// /v1/runs call (dispatchRun), and the sender/topic batch splitters. Route
// resolution + the poll loop live in loop.go (shared with the maintenance
// sweeps).

import (
	"context"
	"log/slog"
	"strings"

	"github.com/kronael/arizuko/core"
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
			for i, m := range msgs {
				ids[i] = m.ID
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
	// else batches per distinct sender, one turn each (mirrors gated
	// processWebTopics / processSenderBatch). The route-pinned topic (r.Topic)
	// overrides the message's own topic for the non-web path.
	var groups [][]core.Message
	if strings.HasPrefix(chatJID, "web:") {
		groups = groupByTopic(trigger)
	} else {
		groups = groupBySender(trigger)
	}

	hadAny := false
	for _, batch := range groups {
		bl := batch[len(batch)-1]
		// A set sticky #topic overrides the message's own topic (gated
		// effectiveTopic); a route-pinned topic is the explicit non-web override.
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
	rendered := l.buildAgentPrompt(folder, topic, trigger)
	// A proactive turn carries one ephemeral <proactive_reason> block ahead
	// of the feed (5/33 § Per-turn envelope); single renderer, dropped after
	// it is consumed so a re-fed turn does not re-attach a stale reason.
	if v, ok := l.pendingReason.LoadAndDelete(turnID); ok {
		pr := v.(proactiveResult)
		rendered = proactiveReasonBlock(pr.check, pr.reason) + rendered
	}

	// A delegated trigger carries forwarded_from = the origin chat JID; persist
	// it as the turn's return address so reply/send/document deliver back to the
	// origin, not the child folder JID the run addresses (gated deliverTo
	// override, gateway.go § processSenderBatch).
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

	out, derr := l.dispatchRun(folder, topic, chatJID, turnID, last.Sender, rendered)
	if derr != nil {
		slog.Warn("dispatch run transport failure", "folder", folder, "turn_id", turnID, "err", derr)
		return false, false, derr
	}
	if out.Steered {
		// The batch was written into the folder's already-live container; the
		// original run owns it and will submit_turn under ITS turn_id. Mark THIS
		// turn_context done so a re-fed poll (or recoverPending after a restart)
		// sees it terminal and skips re-dispatch — else it re-runs as a fresh
		// spawn and duplicates the output (bughunt D-HIGH-2/6).
		_ = l.db.SetTurnState(turnID, "done")
		_ = l.db.SetRunReturned(turnID)
		return true, true, nil
	}
	if out.BreakerOpen {
		slog.Warn("circuit breaker open", "folder", folder, "turn_id", turnID)
	}
	// POST /v1/runs has returned: close the callback surface so a late frame
	// 409s, even if an early submit_turn already flipped state→done.
	_ = l.db.SetRunReturned(turnID)
	if out.Outcome == runedv1.OutcomeError {
		// The run definitively failed. Flag the chat errored and notify it —
		// don't treat a failed run as a silent success (spec 5/E § outcome).
		slog.Warn("run outcome error", "folder", folder, "turn_id", turnID, "err", out.Error)
		_ = l.db.MarkChatErrored(chatJID)
		if l.deliver != nil {
			_, _ = l.deliver.Send(chatJID, runFailureNotice, "", topic, "fail-"+turnID)
		}
		_ = l.db.SetTurnState(turnID, "done")
		return false, false, nil
	}
	// Clean turn-boundary outcome: persist session_id backstop UNLESS
	// submit_turn already recorded one (its value wins; spec 5/E
	// § Completion reconciliation).
	if out.SessionID != "" && !l.db.TurnResultRecorded(folder, turnID) {
		_ = l.db.PutSession(folder, topic, out.SessionID)
	}
	_ = l.db.SetTurnState(turnID, "done")
	return out.Outcome != runedv1.OutcomeSilent, false, nil
}

// groupBySender splits msgs into consecutive same-sender runs, preserving
// causal order: A,B,A yields [A],[B],[A], not [A,A],[B]. Regrouping the whole
// slice by sender would reorder a conversation's turns (mirrors gated
// groupBySender).
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
// by topic would reorder turns across topics (the web: per-topic dispatch;
// mirrors gated processWebTopics).
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
	// Forward the per-group model + container_config (were dropped → every group
	// ran the instance default + ignored custom mounts/timeouts) and the
	// timed-isolated flag (was dropped → one-off runs persisted session lineage
	// they shouldn't). spec 5/E § run request.
	model, containerCfg := l.db.GroupConfig(folder)
	return l.runner.Run(ctx, runedv1.RunRequest{
		Folder:           types.Folder(folder),
		Topic:            topic,
		ChatJID:          chatJID,
		SessionID:        l.db.SessionID(folder, topic),
		MessageBatch:     batch,
		TriggerSender:    trigger,
		CallerSub:        caller,
		TurnID:           turnID,
		CapabilityScopes: l.scopes,
		Model:            model,
		ContainerConfig:  containerCfg,
		Isolated:         strings.HasPrefix(trigger, "timed-isolated:"),
	})
}
