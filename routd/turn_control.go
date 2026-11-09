package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

const deferredWorkPrefix = "\x00turn:"

var errTurnBlocked = errors.New("instance execution paused")

type turnDecision struct {
	retry bool
	trip  bool
	cause string
}

type turnWork struct {
	probe     *store.TurnControlState
	successor bool
}

func (q *turnControl) savePending(l *Loop, tc TurnContext, input, ambient []core.Message) error {
	var err error
	tc.InputKeys, err = turnMessageKeys(input)
	if err != nil {
		return err
	}
	tc.AmbientKeys, err = turnMessageKeys(ambient)
	if err != nil {
		return err
	}
	tc.NextAttemptAt = q.now()
	ctx := context.Background()
	prior, exists := l.db.GetTurnContext(tc.TurnID)
	if exists && prior.State == store.TurnRunning {
		v, busy := q.busyWait.Load(tc.ChatJID)
		if !busy || v.(busyState).lastMsgID != tc.TurnID || prior.RunID != "" || prior.RetryCount != 0 {
			return fmt.Errorf("turn %s still owns execution", tc.TurnID)
		}
		ok, err := l.db.store().RequeueDeferredTurn(ctx, prior, tc.NextAttemptAt)
		if err != nil || !ok {
			return fmt.Errorf("save pending turn %s: requeue refused: %v", tc.TurnID, err)
		}
		return l.db.store().AdvanceAgentCursor(tc.ChatJID, prior.InputLast)
	}
	ok, err := l.db.store().SavePendingTurn(ctx, tc, l.db.GetAgentCursor(tc.ChatJID))
	if err != nil || !ok {
		return fmt.Errorf("save pending turn %s refused: %v", tc.TurnID, err)
	}
	return nil
}

func (q *turnControl) cancel(l *Loop, folder string) error {
	turns, err := l.db.RunningTurns()
	if err != nil {
		return err
	}
	q.mu.Lock()
	activeID := ""
	for _, r := range q.reservations {
		if r.folder == folder {
			activeID = r.turnID
			break
		}
	}
	q.mu.Unlock()
	active, ok := l.db.GetTurnContext(activeID)
	if ok && active.Folder == folder && active.State == store.TurnDone && !active.RunReturned &&
		!slices.ContainsFunc(turns, func(tc TurnContext) bool { return tc.TurnID == activeID }) {
		turns = append(turns, active)
	}
	for _, tc := range turns {
		if tc.Folder != folder {
			continue
		}
		for {
			if tc.State == store.TurnDone && tc.RunReturned {
				break
			}
			before, exists, err := l.db.store().ReadTurnControl(context.Background())
			if err != nil {
				return err
			}
			var prior, after *store.TurnControlState
			if exists {
				prior = &before
				if before.ProbeTurnID == tc.TurnID {
					next := before
					next.Phase, next.Episode = "open", randHex(16)
					next.ProbeTurnID, next.ProbeAttemptID = "", ""
					after = &next
				}
			}
			tc.StatusOwed = "stopped"
			tc.FailureCause = ""
			ok, err := l.db.store().FinishControlledTurn(context.Background(), tc, prior, after)
			if err != nil {
				return err
			}
			current, exists := l.db.GetTurnContext(tc.TurnID)
			if ok || !exists {
				if ok {
					if err := l.publishOwedStatus(current); err != nil {
						return err
					}
				}
				break
			}
			tc = current
		}
	}
	return nil
}

func (q *turnControl) admit(l *Loop, tc TurnContext, admission *store.TurnAdmission, saved *TurnContext) (turnWork, bool, error) {
	ctx := context.Background()
	legacy, err := l.db.store().HeldTurns(ctx, "")
	if err != nil {
		return turnWork{}, false, err
	}
	if len(legacy) > 0 {
		return turnWork{}, false, errTurnBlocked
	}
	pauses, err := l.db.store().ListModelPauses(ctx)
	if err != nil {
		return turnWork{}, false, err
	}
	if slices.ContainsFunc(pauses, func(p store.ModelPause) bool { return p.ProbeTurnID != "" }) {
		return turnWork{}, false, errTurnBlocked
	}
	if prior, ok := l.db.GetTurnContext(tc.TurnID); ok && prior.State == store.TurnDone {
		return turnWork{}, false, nil
	}
	before, exists, err := l.db.store().ReadTurnControl(ctx)
	if err != nil {
		return turnWork{}, false, err
	}
	admission.Controlled = true
	if exists {
		admission.ControlBefore = &before
	}
	work := turnWork{}
	if exists && before.Phase != "closed" {
		if before.Phase != "open" || q.now().Before(before.NextProbeAt) {
			return work, false, errTurnBlocked
		}
		owner, ok := l.db.GetTurnContext(before.ProbeTurnID)
		if ok && owner.State == store.TurnRunning {
			return work, false, errTurnBlocked
		}
		if ok && owner.State == store.TurnPendingRetry && l.db.GroupExists(owner.Folder) {
			if owner.TurnID != tc.TurnID {
				return work, false, errTurnBlocked
			}
		} else {
			oldest, err := q.oldest(l, tc)
			if err != nil {
				return work, false, err
			}
			if oldest != tc.TurnID {
				return work, false, errTurnBlocked
			}
		}
		after := before
		after.Phase, after.ProbeTurnID, after.ProbeAttemptID = "probe", tc.TurnID, admission.AttemptID
		after.Successor = before.Successor || before.ProbeTurnID != tc.TurnID
		admission.ControlAfter = &after
		work.probe, work.successor = &after, after.Successor
	}
	if saved != nil {
		ok, err := l.db.store().ClaimControlledTurn(ctx, *saved, admission.AttemptID, q.now(), admission.ControlBefore, admission.ControlAfter)
		return work, ok, err
	}
	ok, err := l.db.store().OpenTurnContext(tc.TurnID, tc.Folder, tc.Topic, tc.ChatJID,
		tc.Trigger, tc.ReturnTo, admission)
	return work, ok, err
}

func (q *turnControl) oldest(l *Loop, candidate TurnContext) (string, error) {
	ctx := context.Background()
	turns, err := l.db.store().DeferredTurns(ctx, q.now())
	if err != nil {
		return "", err
	}
	var oldest core.Message
	var turnID string
	consider := func(id, folder string, msg core.Message) error {
		if !l.db.GroupExists(folder) {
			return nil
		}
		q.mu.Lock()
		_, active := q.activeFolders[folder]
		q.mu.Unlock()
		if active && id != candidate.TurnID {
			return nil
		}
		if oldest.ID == "" || modelInputBefore(msg, oldest) {
			oldest, turnID = msg, id
		}
		return nil
	}
	for _, tc := range turns {
		msgs, err := l.db.store().DeferredMessages(ctx, tc.InputKeys)
		if err != nil {
			return "", err
		}
		if len(msgs) > 0 {
			if err := consider(tc.TurnID, tc.Folder, msgs[0]); err != nil {
				return "", err
			}
		}
	}
	jids, err := l.db.ChatJIDs()
	if err != nil {
		return "", err
	}
	for _, jid := range jids {
		msgs, batch, _, err := l.pendingBatch(jid, l.db.GetAgentCursor(jid), pollBatch)
		if err != nil {
			return "", err
		}
		if !batch.ok {
			continue
		}
		for i, msg := range msgs {
			if !msg.BotMsg && batch.rows[i].ok && !IsSteerCommand(msg) {
				id := msg.ID
				if jid == candidate.ChatJID && batch.rows[i].Folder == candidate.Folder &&
					store.MessageCursor(msg) <= candidate.InputLast {
					id = candidate.TurnID
				}
				if err := consider(id, batch.rows[i].Folder, msg); err != nil {
					return "", err
				}
			}
		}
	}
	return turnID, nil
}

func classifyTurn(out runedv1.RunOutcome, err error, result store.TurnInfo, replied bool) turnDecision {
	if out.Busy || out.Steered || errors.Is(err, context.Canceled) {
		return turnDecision{}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return turnDecision{trip: true, cause: "Time limit reached"}
	}
	if err == nil && out.Outcome != runedv1.OutcomeError {
		return turnDecision{}
	}
	return turnDecision{trip: true, cause: turnFailureCause(out, err),
		retry: !out.Terminal && !replied && (result.Status == "pending" || result.Status == "")}
}

func (q *turnControl) complete(l *Loop, tc TurnContext, out runedv1.RunOutcome, runErr error, work turnWork) (bool, error) {
	result, err := l.db.store().GetTurnResult(tc.Folder, tc.TurnID)
	if err != nil {
		return false, err
	}
	if tc.State == store.TurnDone && tc.RunReturned && result.Status == "pending" {
		return false, nil
	}
	replied := l.db.TurnHasBotReply(returnTarget(tc, tc.ChatJID), tc.TurnID)
	decision := classifyTurn(out, runErr, result, replied)
	if work.probe != nil && !out.Busy && !out.Steered && runErr == nil && out.Outcome == runedv1.OutcomeOK &&
		result.Status == "pending" && !replied {
		decision = turnDecision{trip: true, cause: "Agent could not complete the request"}
	}
	if tc.ProviderFailureAttempt == tc.AttemptID && !out.Busy && !out.Steered &&
		!errors.Is(runErr, context.Canceled) && !errors.Is(runErr, context.DeadlineExceeded) {
		decision = turnDecision{trip: true, retry: !replied, cause: "Model unavailable"}
	}
	retry := decision.retry && tc.State == store.TurnRunning && tc.RetryCount < l.maxTurnRetry
	resetAt := tc.NextAttemptAt
	tc.NextAttemptAt = maxTime(tc.NextAttemptAt, retryAt(q.now(), tc.Trigger, tc.RetryCount+1))
	if work.successor {
		tc.NextAttemptAt = maxTime(tc.NextAttemptAt, q.now().Add(time.Hour))
	}
	tc.FailureCause = decision.cause
	for {
		before, exists, err := l.db.store().ReadTurnControl(context.Background())
		if err != nil {
			return false, err
		}
		var prior, after *store.TurnControlState
		if exists {
			prior = &before
		}
		owned := before.ProbeTurnID == tc.TurnID &&
			(before.ProbeAttemptID == tc.AttemptID || before.ProbeAttemptID == "")
		if decision.trip {
			next := before
			next.Episode = randHex(16)
			if !exists || before.Phase == "closed" || owned {
				next.Phase, next.ProbeTurnID, next.ProbeAttemptID = "open", "", ""
				next.NextProbeAt = maxTime(resetAt, q.now().Add(time.Hour))
				if retry {
					next.NextProbeAt = tc.NextAttemptAt
					if !work.successor {
						next.ProbeTurnID = tc.TurnID
					}
				}
			}
			after = &next
		} else if owned && work.probe != nil {
			next := before
			next.Phase, next.ProbeAttemptID = "open", ""
			if before.Episode != work.probe.Episode {
				next = before
			}
			if before.Episode == work.probe.Episode && runErr == nil &&
				(out.Outcome == runedv1.OutcomeOK || out.Outcome == runedv1.OutcomeSilent) {
				next = store.TurnControlState{Phase: "closed", Episode: before.Episode}
			}
			after = &next
		}
		var ok bool
		if retry {
			ok, err = l.db.store().DeferTurn(context.Background(), store.DeferredTurn{Turn: tc,
				PriorCursor: l.db.GetAgentCursor(tc.ChatJID), ControlBefore: prior, ControlAfter: after})
		} else if after != nil && (tc.State == store.TurnRunning || tc.State == store.TurnPendingRetry) {
			ok, err = l.db.store().FinishControlledTurn(context.Background(), tc, prior, after)
		} else if after != nil && decision.trip {
			ok, err = l.db.store().RecordControlledStatus(context.Background(), tc, prior, *after)
		} else if after != nil {
			ok, err = l.db.store().WriteTurnControl(context.Background(), prior, *after)
		} else {
			return false, nil
		}
		if err != nil || ok {
			if ok && after != nil && after.Phase == "closed" {
				err = l.db.store().ClearControlledModelPauses(context.Background(), *after)
			}
			if ok {
				current, _ := l.db.GetTurnContext(tc.TurnID)
				if statusErr := l.publishOwedStatus(current); err == nil {
					err = statusErr
				}
			}
			return retry && ok, err
		}
		current, ok := l.db.GetTurnContext(tc.TurnID)
		if !ok || current.State != tc.State || current.AttemptID != tc.AttemptID {
			return false, fmt.Errorf("complete turn %s refused", tc.TurnID)
		}
	}
}

func (q *turnControl) refuse(l *Loop, tc TurnContext, reason string, reset time.Time) (*store.ModelPause, error) {
	ctx := context.Background()
	switch reason {
	case "spend_limit", "rate_limit", "authentication", "outage":
	default:
		return nil, fmt.Errorf("unknown provider failure %q", reason)
	}
	for {
		current, ok := l.db.GetTurnContext(tc.TurnID)
		if !ok || !l.db.GroupExists(tc.Folder) || current.AttemptID != tc.AttemptID || current.RunReturned ||
			current.State != store.TurnRunning || current.ProviderFailureAttempt == tc.AttemptID {
			return nil, fmt.Errorf("turn attempt is closed")
		}
		p, err := l.db.store().ReadModelPause(ctx, tc.Folder)
		if err != nil {
			return nil, err
		}
		next := store.ModelPause{Folder: tc.Folder, OpenedAt: core.TS(q.now())}
		if p != nil {
			next = *p
		}
		next.Reason, next.Evidence, next.Attempts = reason, reason, next.Attempts+1
		next.NextAttemptAt, next.ProbeTurnID, next.ProbeAttemptID = "", "", ""
		before, exists, err := l.db.store().ReadTurnControl(ctx)
		if err != nil {
			return nil, err
		}
		var prior *store.TurnControlState
		if exists {
			prior = &before
		}
		after := before
		after.Episode = randHex(16)
		current.FailureCause = "Model unavailable"
		current.NextAttemptAt = reset
		if !exists || before.Phase == "closed" ||
			(before.ProbeTurnID == tc.TurnID && before.ProbeAttemptID == tc.AttemptID) {
			after.Phase, after.NextProbeAt = "open", maxTime(reset, retryAt(q.now(), tc.Trigger, tc.RetryCount+1))
			after.ProbeTurnID, after.ProbeAttemptID = tc.TurnID, tc.AttemptID
		}
		ok, err = l.db.store().RecordControlledProviderFailure(ctx, current, p, &next, prior, &after)
		if err != nil || ok {
			return &next, err
		}
	}
}

func maxTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return b
	}
	return a
}

func (q *turnControl) rearm(l *Loop, probe store.TurnControlState) error {
	after := probe
	after.Phase, after.ProbeAttemptID = "open", ""
	_, err := l.db.store().WriteTurnControl(context.Background(), &probe, after)
	return err
}

func (q *turnControl) interrupt(l *Loop, tc TurnContext) (store.TurnControlState, error) {
	for {
		before, exists, err := l.db.store().ReadTurnControl(context.Background())
		if err != nil {
			return store.TurnControlState{}, err
		}
		after := before
		var prior *store.TurnControlState
		if exists {
			prior = &before
		}
		if !exists || before.Phase == "closed" {
			after = store.TurnControlState{NextProbeAt: q.now().Add(time.Hour),
				ProbeTurnID: tc.TurnID, ProbeAttemptID: tc.AttemptID}
			if tc.State == store.TurnHeld || tc.ProbeEpisode != "" {
				after.NextProbeAt = retryAt(q.now(), tc.Trigger, tc.RetryCount+1)
			}
		}
		after.Phase, after.Episode = "probe", randHex(16)
		ok, err := l.db.store().WriteTurnControl(context.Background(), prior, after)
		if err != nil || ok {
			return after, err
		}
	}
}

func retryAt(now time.Time, sender string, retry int) time.Time {
	delays := [...]time.Duration{30 * time.Second, 2 * time.Minute, 5 * time.Minute}
	if core.IsScheduledSender(sender) {
		delays = [...]time.Duration{15 * time.Minute, 45 * time.Minute, 180 * time.Minute}
	}
	return now.Add(delays[min(max(retry, 1), len(delays))-1])
}

func (q *turnControl) tick(l *Loop) {
	turns, err := l.db.store().DeferredTurns(context.Background(), q.now())
	if err != nil {
		slog.Error("select deferred turns", "err", err)
		return
	}
	for _, tc := range turns {
		l.Enqueue(deferredWorkPrefix + tc.TurnID)
	}
}

func (l *Loop) replay(turnID string) (bool, error) {
	ctx := context.Background()
	tc, ok, err := l.db.store().ReadTurnContext(ctx, turnID)
	if err != nil || !ok || tc.State != store.TurnPendingRetry || l.q.now().Before(tc.NextAttemptAt) {
		return false, err
	}
	trigger, err := l.db.store().DeferredMessages(ctx, tc.InputKeys)
	if err != nil {
		return false, fmt.Errorf("replay turn %s: %w", turnID, err)
	}
	if len(trigger) == 0 {
		return false, fmt.Errorf("replay turn %s: no saved input", turnID)
	}
	ambient, err := l.db.store().DeferredMessages(ctx, tc.AmbientKeys)
	if err != nil {
		return false, fmt.Errorf("replay turn %s: %w", turnID, err)
	}
	last := &trigger[len(trigger)-1]
	last.Sender, last.ForwardedFrom = tc.Trigger, tc.ReturnTo
	had, _, _, mark, err := l.runBoundTurn(tc.Folder, tc.Topic, tc.ChatJID,
		tc.TurnID, tc.Addressed, trigger, ambient, &tc)
	if err == nil && mark != "" {
		l.advanceObserved(tc.Folder, []observedAdvance{{topic: tc.Topic, ts: mark}})
	}
	return had, err
}

func (l *Loop) migrateRetries(chatJID string, includeHeld bool) error {
	turns, err := l.db.RunningTurns()
	if err != nil {
		return err
	}
	if includeHeld {
		held, err := l.db.store().HeldTurns(context.Background(), chatJID)
		if err != nil {
			return err
		}
		turns = append(turns, held...)
	}
	for _, tc := range turns {
		if tc.ChatJID != chatJID || (tc.State != store.TurnPendingRetry && tc.State != store.TurnHeld) || !tc.NextAttemptAt.IsZero() {
			continue
		}
		cursor := l.db.GetAgentCursor(chatJID)
		if tc.State == store.TurnHeld && !tc.RunReturned {
			return fmt.Errorf("legacy provider execution %s is unresolved", tc.TurnID)
		}
		if len(tc.InputKeys) > 0 {
			tc.NextAttemptAt = retryAt(l.q.now(), tc.Trigger, tc.RetryCount)
			ok, err := l.migrateBoundTurn(tc, cursor)
			if err != nil || !ok {
				return fmt.Errorf("migrate turn %s: saved binding refused: %v", tc.TurnID, err)
			}
			continue
		}
		msgs, batch, _, err := l.pendingBatch(chatJID, cursor, pollBatch)
		if err != nil {
			return err
		}
		end := slices.IndexFunc(msgs, func(m core.Message) bool {
			return m.ID == tc.TurnID || (tc.State == store.TurnHeld && store.MessageCursor(m) == tc.InputLast)
		})
		if end < 0 {
			return fmt.Errorf("migrate turn %s: input boundary is missing", tc.TurnID)
		}
		msgs = msgs[:end+1]
		_, pinnedTopic := l.db.StickyState(chatJID)
		var trigger, ambient []core.Message
		for i, msg := range msgs {
			if msg.BotMsg {
				continue
			}
			if !batch.rows[i].ok {
				ambient = append(ambient, msg)
				continue
			}
			topic := msg.Topic
			if !batch.rows[i].TopicPinned && pinnedTopic != "" {
				topic = pinnedTopic
			}
			if tc.State == store.TurnHeld {
				topic = tc.Topic
			}
			if batch.rows[i].Folder != tc.Folder || msg.Sender != tc.Trigger || topic != tc.Topic || IsSteerCommand(msg) {
				return fmt.Errorf("migrate turn %s: ambiguous input binding", tc.TurnID)
			}
			trigger = append(trigger, msg)
		}
		if len(trigger) == 0 {
			return fmt.Errorf("migrate turn %s: no resolved input", tc.TurnID)
		}
		tc.InputKeys, err = turnMessageKeys(trigger)
		if err != nil {
			return err
		}
		tc.AmbientKeys, err = turnMessageKeys(ambient)
		if err != nil {
			return err
		}
		tc.InputLast = store.MessageCursor(msgs[len(msgs)-1])
		tc.NextAttemptAt = retryAt(l.q.now(), tc.Trigger, tc.RetryCount)
		ok, err := l.migrateBoundTurn(tc, cursor)
		if err != nil || !ok {
			return fmt.Errorf("migrate turn %s: binding refused: %v", tc.TurnID, err)
		}
	}
	return nil
}

func (l *Loop) migrateBoundTurn(tc TurnContext, cursor string) (bool, error) {
	control, exists, err := l.db.store().ReadTurnControl(context.Background())
	if err != nil {
		return false, err
	}
	var before *store.TurnControlState
	if exists {
		before = &control
	}
	d := store.DeferredTurn{Turn: tc, PriorCursor: cursor, ControlBefore: before}
	if tc.State == store.TurnHeld {
		d.ReserveRetry = tc.RetryCount < l.maxTurnRetry
		d.Turn.NextAttemptAt = retryAt(l.q.now(), tc.Trigger, tc.RetryCount+1)
		p, err := l.db.store().ReadModelPause(context.Background(), tc.Folder)
		if err != nil {
			return false, err
		}
		if p != nil && p.NextAttemptAt != "" {
			reset, err := time.Parse(time.RFC3339Nano, p.NextAttemptAt)
			if err != nil {
				return false, err
			}
			d.Turn.NextAttemptAt = maxTime(d.Turn.NextAttemptAt, reset)
		}
		if before == nil || before.Phase == "closed" || before.ProbeTurnID == tc.TurnID {
			d.ControlAfter = &store.TurnControlState{Phase: "open", Episode: randHex(16),
				ProbeTurnID: tc.TurnID, NextProbeAt: d.Turn.NextAttemptAt}
		}
	}
	ok, err := l.db.store().MigrateDeferredTurn(context.Background(), d)
	if err == nil && ok && tc.State == store.TurnHeld && !d.ReserveRetry {
		current, _ := l.db.GetTurnContext(tc.TurnID)
		_, err = l.q.complete(l, current, runedv1.RunOutcome{Outcome: runedv1.OutcomeError, Terminal: true}, nil, turnWork{})
	}
	return ok, err
}

func (q *turnControl) recoverProvider(l *Loop, startup bool) error {
	pauses, err := l.db.store().ListModelPauses(context.Background())
	if err != nil {
		return err
	}
	for _, p := range pauses {
		if p.ProbeTurnID == "" {
			continue
		}
		tc, ok := l.db.GetTurnContext(p.ProbeTurnID)
		if !ok || tc.AttemptID != p.ProbeAttemptID || tc.ProbeEpisode != p.OpenedAt {
			return fmt.Errorf("legacy model probe %s has no matching attempt", p.ProbeTurnID)
		}
		_, err := q.WithIdleFolder(tc.Folder, func() error {
			control, err := q.interrupt(l, tc)
			if err != nil {
				return err
			}
			if !startup && !tc.RunReturned {
				return nil
			}
			if err := l.db.SetRunReturned(tc.TurnID); err != nil {
				return err
			}
			if err := l.stopModelProbe(tc); err != nil {
				return err
			}
			if err := l.db.store().RetireModelProbe(context.Background(), p, tc); err != nil {
				return err
			}
			if tc.State == store.TurnRunning {
				if err := l.migrateRetries(tc.ChatJID, true); err != nil {
					return err
				}
			}
			return q.rearm(l, control)
		})
		if err != nil {
			return err
		}
	}
	held, err := l.db.store().HeldTurns(context.Background(), "")
	if err != nil {
		return err
	}
	for _, tc := range held {
		_, err := q.WithIdleFolder(tc.Folder, func() error {
			if _, err := q.interrupt(l, tc); err != nil {
				return err
			}
			if err := l.db.SetRunReturned(tc.TurnID); err != nil {
				return err
			}
			if err := l.stopModelProbe(tc); err != nil {
				return err
			}
			return l.migrateRetries(tc.ChatJID, true)
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func (l *Loop) isSavedExecution(tc TurnContext) bool {
	return len(tc.InputKeys) > 0 && tc.InputLast != "" && tc.InputLast <= l.db.GetAgentCursor(tc.ChatJID)
}

func (q *turnControl) recover(l *Loop, startup bool) error {
	if err := q.recoverProvider(l, startup); err != nil {
		return err
	}
	control, _, err := l.db.store().ReadTurnControl(context.Background())
	if err != nil {
		return err
	}
	turns, err := l.db.RunningTurns()
	if err != nil {
		return err
	}
	if control.Phase == "probe" && control.ProbeAttemptID != "" {
		probe, ok := l.db.GetTurnContext(control.ProbeTurnID)
		if !ok {
			return fmt.Errorf("interrupted probe has no turn %s", control.ProbeTurnID)
		}
		if !slices.ContainsFunc(turns, func(tc TurnContext) bool { return tc.TurnID == probe.TurnID }) {
			turns = append(turns, probe)
		}
	}
	blocked := false
	recovery := control
	for _, tc := range turns {
		if tc.State == store.TurnPendingRetry && tc.NextAttemptAt.IsZero() {
			if err := l.migrateRetries(tc.ChatJID, false); err != nil {
				return err
			}
		}
		isProbe := control.Phase == "probe" && control.ProbeTurnID == tc.TurnID && control.ProbeAttemptID == tc.AttemptID
		if !isProbe && (tc.State != store.TurnRunning ||
			(!l.isSavedExecution(tc) && !tc.RunReturned && !(startup && len(tc.InputKeys) > 0))) {
			continue
		}
		ok, err := q.WithIdleFolder(tc.Folder, func() error {
			var err error
			recovery, err = q.interrupt(l, tc)
			if err != nil {
				return err
			}
			if err := l.db.SetRunReturned(tc.TurnID); err != nil {
				return err
			}
			if err := l.stopModelProbe(tc); err != nil {
				return err
			}
			if tc.State == store.TurnDone {
				return nil
			}
			deferred, err := q.complete(l, tc, runedv1.RunOutcome{Outcome: runedv1.OutcomeError}, nil,
				turnWork{successor: isProbe && control.Successor})
			if err == nil && !deferred {
				finishTurn(l.db, l.deliver, tc.TurnID, endFailed)
			}
			return err
		})
		if err != nil {
			return err
		}
		blocked = blocked || !ok
	}
	if !blocked && recovery.Phase == "probe" {
		return q.rearm(l, recovery)
	}
	return nil
}

var ErrDependencyDown = errors.New("execution plane unreachable")

type groupState struct {
	active bool
}

type turnReservation struct {
	folder string
	cron   bool
	turnID string
}

const (
	busyBackoffBase = 4 * time.Second
	busyBackoffCap  = 60 * time.Second
)

type busyState struct {
	until     time.Time
	delay     time.Duration
	lastMsgID string
}

func (q *turnControl) busyDeferred(chatJID, lastMsgID string) bool {
	v, ok := q.busyWait.Load(chatJID)
	if !ok {
		return false
	}
	st := v.(busyState)
	if st.lastMsgID != lastMsgID {
		q.busyWait.Delete(chatJID)
		return false
	}
	return q.now().Before(st.until)
}

func (q *turnControl) noteBusy(chatJID, lastMsgID string) time.Time {
	delay := busyBackoffBase
	if v, ok := q.busyWait.Load(chatJID); ok {
		if st := v.(busyState); st.lastMsgID == lastMsgID {
			delay = min(st.delay*2, busyBackoffCap)
		}
	}
	until := q.now().Add(delay)
	q.busyWait.Store(chatJID, busyState{until: until, delay: delay, lastMsgID: lastMsgID})
	return until
}

type processMessagesFn func(groupJid string) (bool, error)
type folderForJidFn func(groupJid string) string

// parkedFn is told, outside q.mu, each time EnqueueMessageCheck parks a chat
// behind another run (its folder is held, or the run cap is full).
type parkedFn func(groupJid string)

type turnControl struct {
	mu              sync.Mutex
	groups          map[string]*groupState
	activeCount     int
	activeCron      int
	activeFolders   map[string]string
	reservations    map[string]turnReservation
	pending         map[string]turnReservation
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	folderForJid    folderForJidFn
	parked          parkedFn
	shuttingDown    bool
	busyWait        sync.Map
	now             func() time.Time
}

func newTurnControl(maxConcurrent int) *turnControl {
	return &turnControl{
		groups:        make(map[string]*groupState),
		activeFolders: make(map[string]string),
		reservations:  make(map[string]turnReservation),
		pending:       make(map[string]turnReservation),
		maxConcurrent: maxConcurrent,
		now:           time.Now,
	}
}

func (q *turnControl) getGroup(groupJid string) *groupState {
	s := q.groups[groupJid]
	if s == nil {
		s = &groupState{}
		q.groups[groupJid] = s
	}
	return s
}

func (q *turnControl) SetProcessMessagesFn(fn processMessagesFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.processMessages = fn
}
func (q *turnControl) SetFolderForJidFn(fn folderForJidFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.folderForJid = fn
}

func (q *turnControl) SetParkedFn(fn parkedFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.parked = fn
}

// WithIdleFolder runs fn while admission to an idle folder is excluded.
func (q *turnControl) WithIdleFolder(folder string, fn func() error) (bool, error) {
	q.mu.Lock()
	if _, busy := q.activeFolders[folder]; busy {
		q.mu.Unlock()
		return false, nil
	}
	q.activeFolders[folder] = ""
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.activeFolders, folder)
		if !q.shuttingDown {
			q.drainWaitingLocked()
		}
		q.mu.Unlock()
	}()
	return true, fn()
}

func (q *turnControl) folderOf(jid string) string {
	if q.folderForJid == nil {
		return ""
	}
	return q.folderForJid(jid)
}

func (q *turnControl) activateLocked(jid string) {
	q.getGroup(jid).active = true
}

func (q *turnControl) canReserve(r turnReservation) bool {
	_, busy := q.activeFolders[r.folder]
	return !busy && q.activeCount < q.maxConcurrent && (!r.cron || q.activeCron == 0)
}

func (q *turnControl) reserveLocked(jid string, r turnReservation) {
	q.reservations[jid] = r
	q.activeCount++
	if r.cron {
		q.activeCron++
	}
	q.activeFolders[r.folder] = jid
}

func (q *turnControl) reserve(jid, folder string, cron bool, turnID string) bool {
	q.mu.Lock()
	r := turnReservation{folder: folder, cron: cron, turnID: turnID}
	prior, reserved := q.reservations[jid]
	if reserved && prior != r {
		q.releaseLocked(jid)
		reserved = false
	}
	if reserved || q.canReserve(r) {
		delete(q.pending, jid)
		q.waitingGroups = slices.DeleteFunc(q.waitingGroups, func(key string) bool { return key == jid })
		if !reserved {
			q.reserveLocked(jid, r)
		}
		q.mu.Unlock()
		return true
	}
	q.pending[jid] = r
	if !slices.Contains(q.waitingGroups, jid) {
		q.waitingGroups = append(q.waitingGroups, jid)
	}
	fn := q.parked
	q.mu.Unlock()
	if fn != nil {
		fn(jid)
	}
	return false
}

func (q *turnControl) releaseLocked(jid string) {
	r, ok := q.reservations[jid]
	if !ok {
		return
	}
	delete(q.reservations, jid)
	delete(q.activeFolders, r.folder)
	q.activeCount--
	if r.cron {
		q.activeCron--
	}
}

func (q *turnControl) release(jid string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.releaseLocked(jid)
	q.drainWaitingLocked()
}

func (q *turnControl) EnqueueMessageCheck(groupJid string) {
	q.mu.Lock()
	if q.shuttingDown {
		q.mu.Unlock()
		return
	}

	s := q.getGroup(groupJid)

	if s.active {
		slog.Debug("container active, will drain after", "groupJid", groupJid)
		q.mu.Unlock()
		return
	}

	q.activateLocked(groupJid)
	q.mu.Unlock()

	go q.runForGroup(groupJid)
}

// Shutdown stops admitting work. A live container is runed's to reap: the
// queue paces dispatch and holds no container handle.
func (q *turnControl) Shutdown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	slog.Info("GroupQueue shutting down (containers detached, not killed)",
		"activeCount", q.activeCount)
}

func (q *turnControl) runForGroup(groupJid string) {
	q.mu.Lock()
	fn := q.processMessages
	slog.Debug("starting container for group", "groupJid", groupJid, "activeCount", q.activeCount)
	q.mu.Unlock()

	var err error
	if fn != nil {
		_, err = fn(groupJid)
	}

	if err != nil {
		slog.Error("error processing messages for group",
			"groupJid", groupJid, "err", err)
	}
	q.mu.Lock()
	s := q.getGroup(groupJid)
	s.active = false
	q.releaseLocked(groupJid)
	if !q.shuttingDown {
		q.drainWaitingLocked()
	}
	q.mu.Unlock()
}

func (q *turnControl) drainWaitingLocked() {
	i := 0
	for !q.shuttingDown && i < len(q.waitingGroups) && q.activeCount < q.maxConcurrent {
		jid := q.waitingGroups[i]
		r := q.pending[jid]
		if q.getGroup(jid).active || !q.canReserve(r) {
			i++
			continue
		}
		q.waitingGroups = append(q.waitingGroups[:i], q.waitingGroups[i+1:]...)
		delete(q.pending, jid)
		q.reserveLocked(jid, r)
		q.activateLocked(jid)
		go q.runForGroup(jid)
	}
}

func (q *turnControl) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeCount
}
