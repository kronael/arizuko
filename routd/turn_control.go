package routd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
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
	prior, exists, err := l.db.store().ReadTurnContext(ctx, tc.TurnID)
	if err != nil {
		return err
	}
	if exists && prior.State == store.TurnWaitingCapacity {
		if prior.RunID != "" || prior.RunReturned || prior.RetryCount != 0 {
			return fmt.Errorf("turn %s still owns execution", tc.TurnID)
		}
		ok, err := l.db.store().RequeueDeferredTurn(ctx, prior, tc.NextAttemptAt, l.db.GetAgentCursor(tc.ChatJID))
		if err != nil || !ok {
			return fmt.Errorf("save pending turn %s: requeue refused: %v", tc.TurnID, err)
		}
		return nil
	}
	if exists && prior.State == store.TurnRunning {
		return fmt.Errorf("turn %s still owns execution", tc.TurnID)
	}
	ok, err := l.db.store().SavePendingTurn(ctx, tc, l.db.GetAgentCursor(tc.ChatJID))
	if err != nil || !ok {
		return fmt.Errorf("save pending turn %s refused: %v", tc.TurnID, err)
	}
	return nil
}

func (q *turnControl) cancel(l *Loop, folder string) ([]string, error) {
	unlock := lockTurn("\x00checkpoint:" + folder)
	defer unlock()
	var canceled []string
	turns, err := l.db.RunningTurns()
	if err != nil {
		return canceled, err
	}
	q.mu.Lock()
	var activeIDs []string
	for _, r := range q.reservations {
		if r.folder == folder {
			activeIDs = append(activeIDs, r.turnID)
		}
	}
	q.mu.Unlock()
	for _, activeID := range activeIDs {
		active, ok := l.db.GetTurnContext(activeID)
		if ok && active.Folder == folder && active.State == store.TurnDone && !active.RunReturned &&
			!slices.ContainsFunc(turns, func(tc TurnContext) bool { return tc.TurnID == activeID }) {
			turns = append(turns, active)
		}
	}
	for _, tc := range turns {
		if tc.Folder != folder {
			continue
		}
		for tc.State != store.TurnDone || !tc.RunReturned {
			before, exists, err := l.db.store().ReadTurnControl(context.Background())
			if err != nil {
				return canceled, err
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
				return canceled, err
			}
			current, exists := l.db.GetTurnContext(tc.TurnID)
			if ok || !exists {
				if ok {
					canceled = append(canceled, current.TurnID)
					if err := l.publishOwedStatus(current); err != nil {
						return canceled, err
					}
				}
				break
			}
			tc = current
		}
	}
	return canceled, nil
}

func (q *turnControl) admit(l *Loop, tc TurnContext, admission *store.TurnAdmission, saved *TurnContext) (turnWork, bool, error) {
	ctx := context.Background()
	prior, priorExists, err := l.db.store().ReadTurnContext(ctx, tc.TurnID)
	if err != nil {
		return turnWork{}, false, err
	}
	if priorExists && prior.State == store.TurnDone {
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
	held := exists && before.Phase != "closed" && (before.Folder == "" || before.Folder == tc.Folder)
	if held {
		if before.Phase != "open" || q.now().Before(before.NextProbeAt) {
			return work, false, errTurnBlocked
		}
		var owner TurnContext
		var ok bool
		if before.ProbeTurnID != "" {
			owner, ok, err = l.db.store().ReadTurnContext(ctx, before.ProbeTurnID)
			if err != nil {
				return work, false, err
			}
		}
		if ok && owner.State == store.TurnRunning {
			return work, false, errTurnBlocked
		}
		// Under a breaker that holds every folder, the folder of a due probe
		// owner probes first. The folder's own input order, checked before
		// admission, picks the turn.
		ownerIsDue := ok && (owner.State == store.TurnPendingRetry || owner.State == store.TurnWaitingCapacity) &&
			l.db.GroupExists(owner.Folder) && !q.now().Before(owner.NextAttemptAt)
		if before.Folder == "" && ownerIsDue && owner.Folder != tc.Folder {
			return work, false, errTurnBlocked
		}
		after := before
		after.Phase, after.ProbeTurnID, after.ProbeAttemptID = "probe", tc.TurnID, admission.AttemptID
		after.Successor = before.Successor || before.ProbeTurnID != tc.TurnID
		admission.ControlAfter = &after
		work.probe, work.successor = &after, after.Successor
	}
	if saved != nil {
		ok, err := l.db.store().ClaimControlledTurn(ctx, *saved, nil, admission, q.now())
		return work, ok, err
	}
	if priorExists && prior.State == store.TurnWaitingCapacity {
		ok, err := l.db.store().ClaimControlledTurn(ctx, prior, &tc, admission, q.now())
		return work, ok, err
	}
	ok, err := l.db.store().OpenTurnContext(tc.TurnID, tc.Folder, tc.Topic, tc.ChatJID,
		tc.Trigger, tc.ReturnTo, admission)
	return work, ok, err
}

func classifyTurn(out runedv1.RunOutcome, err error, result store.TurnInfo, replied bool) turnDecision {
	if out.Busy || errors.Is(err, context.Canceled) {
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
	ceiling := out.TimedOut || errors.Is(runErr, context.DeadlineExceeded)
	provider := tc.AttemptID != "" && tc.ProviderFailureAttempt == tc.AttemptID
	checkpoint := result.Status == "checkpoint" ||
		ceiling && result.Status != "success" && result.Status != "refused" && result.Status != "budget"
	if checkpoint && !provider {
		if result.Status == "pending" || result.Status == "" {
			sessionID := tc.SessionID
			if !core.IsIsolatedSender(tc.Trigger) {
				sessionID = l.db.SessionID(tc.Folder, tc.Topic)
			}
			if _, err := l.db.RecordTurnResult(tc.Folder, tc.TurnID, sessionID, "checkpoint", "", store.TurnUsage{}); err != nil {
				return false, err
			}
		}
		if work.probe != nil {
			closed := store.TurnControlState{Phase: "closed", Episode: work.probe.Episode}
			ok, err := l.db.store().WriteTurnControl(context.Background(), work.probe, closed)
			if err != nil {
				return false, err
			}
			if ok {
				if err := l.db.store().ClearControlledModelPauses(context.Background(), closed); err != nil {
					return false, err
				}
			}
		}
		return true, l.continueTurn(tc)
	}
	if tc.State == store.TurnDone && tc.RunReturned && result.Status == "pending" {
		return false, nil
	}
	replied := l.db.TurnHasBotReply(returnTarget(tc, tc.ChatJID), tc.TurnID)
	providerOut := out
	if provider {
		providerOut.Outcome = runedv1.OutcomeError
	}
	decision := classifyTurn(providerOut, runErr, result, replied)
	if work.probe != nil && !out.Busy && runErr == nil && providerOut.Outcome == runedv1.OutcomeOK &&
		result.Status == "pending" && !replied {
		decision = turnDecision{trip: true, cause: "Agent could not complete the request"}
	}
	if provider && decision.trip && !errors.Is(runErr, context.DeadlineExceeded) {
		decision.cause = "Model unavailable"
	}
	failure := retry.FromError(runErr, q.now())
	if provider {
		failure = retry.Failure{Kind: tc.ProviderFailureKind, RetryAt: tc.NextAttemptAt}
	} else {
		if runErr == nil && out.Outcome == runedv1.OutcomeError && !out.Terminal {
			failure.Kind = "transient"
		}
		failure.RetryAt = maxTime(failure.RetryAt, tc.NextAttemptAt)
	}
	retryDecision := retry.Next(tc.RetryCount, failure, q.now(), time.Time{})
	shouldRetry := decision.retry && tc.State == store.TurnRunning && !retryDecision.Stop
	resetAt := tc.NextAttemptAt
	tc.NextAttemptAt = retryDecision.NextAt
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
			opens := !exists || before.Phase == "closed"
			if opens {
				next.Folder = tc.Folder
			}
			if opens || owned {
				next.Phase, next.ProbeTurnID, next.ProbeAttemptID = "open", "", ""
				next.NextProbeAt = maxTime(resetAt, q.now().Add(time.Hour))
				if opens && !decision.retry {
					first := retry.Next(0, retry.Failure{Kind: "transient"}, q.now(), time.Time{})
					next.NextProbeAt = maxTime(resetAt, first.NextAt)
				}
				if shouldRetry {
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
		if shouldRetry {
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
			return shouldRetry && ok, err
		}
		current, ok := l.db.GetTurnContext(tc.TurnID)
		if !ok || current.State != tc.State || current.AttemptID != tc.AttemptID {
			return false, fmt.Errorf("complete turn %s refused", tc.TurnID)
		}
	}
}

func (q *turnControl) refuse(l *Loop, tc TurnContext, reason string, reset time.Time) (*store.ModelPause, error) {
	ctx := context.Background()
	now := q.now()
	failure := retry.ProviderFailure(reason, reset, now)
	for {
		current, ok, err := l.db.store().ReadTurnContext(ctx, tc.TurnID)
		if err != nil {
			return nil, err
		}
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
		next.NextAttemptAt = ""
		before, exists, err := l.db.store().ReadTurnControl(ctx)
		if err != nil {
			return nil, err
		}
		var prior *store.TurnControlState
		if exists {
			prior = &before
		}
		after := before
		after.Episode, after.Folder = randHex(16), ""
		current.FailureCause = "Model unavailable"
		current.NextAttemptAt = failure.RetryAt
		current.ProviderFailureKind = failure.Kind
		if !exists || before.Phase == "closed" ||
			(before.ProbeTurnID == tc.TurnID && before.ProbeAttemptID == tc.AttemptID) {
			decision := retry.Next(current.RetryCount, failure, now, time.Time{})
			nextProbe := decision.NextAt
			if decision.Stop {
				nextProbe = maxTime(failure.RetryAt, now.Add(time.Hour))
			}
			after.Phase, after.NextProbeAt = "open", nextProbe
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
	// A refused rearm leaves the newer control generation pending.
	_, err := l.db.store().WriteTurnControl(context.Background(), &probe, after)
	return err
}

func (q *turnControl) interrupt(l *Loop, tc TurnContext, expected ...store.TurnControlState) (store.TurnControlState, error) {
	before, exists, err := l.db.store().ReadTurnControl(context.Background())
	if err != nil {
		return store.TurnControlState{}, err
	}
	if len(expected) > 0 && (!exists || before != expected[0]) {
		return store.TurnControlState{}, store.ErrRefused
	}
	after := before
	var prior *store.TurnControlState
	if exists {
		prior = &before
	}
	if !exists || before.Phase == "closed" {
		after = store.TurnControlState{Folder: tc.Folder, NextProbeAt: q.now().Add(time.Hour),
			ProbeTurnID: tc.TurnID, ProbeAttemptID: tc.AttemptID}
	}
	after.Phase, after.Episode = "probe", randHex(16)
	ok, err := l.db.store().InterruptTurn(context.Background(), tc, prior, after)
	if err == nil && !ok {
		err = store.ErrRefused
	}
	return after, err
}

func (q *turnControl) tick(l *Loop) {
	if err := q.recover(l, false); err != nil {
		slog.Error("recover deferred turns", "err", err)
	}
	turns, err := l.db.store().DeferredTurns(context.Background(), q.now())
	if err != nil {
		slog.Error("select deferred turns", "err", err)
		return
	}
	for _, tc := range turns {
		if (tc.State != store.TurnPendingRetry && tc.State != store.TurnWaitingCapacity) || q.now().Before(tc.NextAttemptAt) {
			continue
		}
		if tc.State == store.TurnWaitingCapacity && !l.isSavedExecution(tc) {
			l.Enqueue(tc.ChatJID)
		} else {
			l.Enqueue(deferredWorkPrefix + tc.TurnID)
		}
	}
}

func (q *turnControl) wakeRefused() {
	var refused []string
	q.mu.Lock()
	for jid, s := range q.groups {
		if s.refused || s.candidate {
			refused = append(refused, jid)
		}
	}
	q.mu.Unlock()
	for _, jid := range refused {
		q.EnqueueMessageCheck(jid)
	}
}

func (l *Loop) replay(turnID string) (bool, error) {
	ctx := context.Background()
	tc, ok, err := l.db.store().ReadTurnContext(ctx, turnID)
	if err != nil || !ok || (tc.State != store.TurnPendingRetry && tc.State != store.TurnWaitingCapacity) || l.q.now().Before(tc.NextAttemptAt) || (tc.State == store.TurnWaitingCapacity && !l.isSavedExecution(tc)) {
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
	if strings.HasPrefix(tc.TurnID, core.CheckpointPrefix) {
		last.BotMsg = false
	}
	had, _, mark, err := l.runBoundTurn(tc.Folder, tc.Topic, tc.ChatJID,
		tc.TurnID, tc.Addressed, trigger, ambient, &tc, tc.CapacityWakeCursor)
	if errors.Is(err, errModelInputOrder) {
		return false, nil
	}
	if err == nil && mark != "" {
		l.advanceObserved(tc.Folder, []observedAdvance{{topic: tc.Topic, ts: mark}})
	}
	return had, err
}

func (l *Loop) isSavedExecution(tc TurnContext) bool {
	return len(tc.InputKeys) > 0 && tc.InputLast != "" && tc.InputLast <= l.db.GetAgentCursor(tc.ChatJID)
}

func (q *turnControl) recover(l *Loop, startup bool) error {
	ctx := context.Background()
	work, err := l.db.store().DueWork(ctx, "control", q.now())
	if err != nil || !startup && len(work) == 0 {
		return err
	}
	var turns []TurnContext
	if startup {
		turns, err = l.db.RunningTurns()
	} else {
		turns, err = l.db.store().RecoverableTurns(context.Background())
	}
	if err != nil {
		return err
	}
	control, _, err := l.db.store().ReadTurnControl(context.Background())
	if err != nil {
		return err
	}
	if control.Phase == "probe" && control.ProbeAttemptID != "" {
		probe, ok, err := l.db.store().ReadTurnContext(ctx, control.ProbeTurnID)
		if err != nil {
			return err
		}
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
		isProbe := control.Phase == "probe" && control.ProbeTurnID == tc.TurnID && control.ProbeAttemptID == tc.AttemptID
		if !isProbe && (tc.State != store.TurnRunning ||
			(!l.isSavedExecution(tc) && !tc.RunReturned && (!startup || len(tc.InputKeys) == 0))) {
			continue
		}
		if isProbe && tc.State == store.TurnWaitingCapacity {
			if err := q.rearm(l, control); err != nil {
				return err
			}
			recovery.Phase = "open"
			continue
		}
		ok, err := q.WithIdleTopic(tc.Folder, tc.Topic, func() error {
			result, err := l.db.store().GetTurnResult(tc.Folder, tc.TurnID)
			if err != nil {
				return err
			}
			if result.Status == "checkpoint" {
				if err := l.stopModelProbe(tc); err != nil {
					return err
				}
				if err := l.db.SetRunReturned(tc.TurnID); err != nil {
					return err
				}
				return l.continueTurn(tc)
			}
			var expected []store.TurnControlState
			if isProbe {
				expected = append(expected, control)
			}
			recovery, err = q.interrupt(l, tc, expected...)
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
	due := time.Time{}
	if blocked || control.Phase == "probe" {
		due = q.now().Add(30 * time.Second)
	} else if control.Phase == "open" && q.now().Before(control.NextProbeAt) {
		due = control.NextProbeAt
	} else {
		q.wakeRefused()
	}
	for _, it := range work {
		if due.IsZero() {
			err = l.db.store().AcknowledgeWork(ctx, it)
		} else {
			err = l.db.store().RescheduleWork(ctx, it, due)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var ErrDependencyDown = errors.New("execution plane unreachable")

type groupState struct {
	processFailures int
	active          bool
	again           bool
	refused         bool
	candidate       bool
}

type turnReservation struct {
	folder string
	topic  string
	cron   bool
	turnID string
}

type topicKey struct{ folder, topic string }

func (r turnReservation) key() topicKey { return topicKey{folder: r.folder, topic: r.topic} }

type processMessagesFn func(groupJid string) (bool, error)

type parkedFn func(groupJid string)

type turnControl struct {
	mu              sync.Mutex
	groups          map[string]*groupState
	activeCount     int
	activeCron      int
	activeTopics    map[topicKey]string
	reservations    map[string]turnReservation
	pending         map[string]turnReservation
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	parked          parkedFn
	shuttingDown    bool
	now             func() time.Time
}

func newTurnControl(maxConcurrent int) *turnControl {
	return &turnControl{
		groups:        make(map[string]*groupState),
		activeTopics:  make(map[topicKey]string),
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
func (q *turnControl) SetParkedFn(fn parkedFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.parked = fn
}

// WithIdleTopic runs fn while admission to an idle (folder, topic) is excluded.
func (q *turnControl) WithIdleTopic(folder, topic string, fn func() error) (bool, error) {
	key := topicKey{folder: folder, topic: topic}
	q.mu.Lock()
	if _, busy := q.activeTopics[key]; busy {
		q.mu.Unlock()
		return false, nil
	}
	q.activeTopics[key] = ""
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		delete(q.activeTopics, key)
		if !q.shuttingDown {
			q.drainWaitingLocked()
		}
		q.mu.Unlock()
	}()
	return true, fn()
}

func (q *turnControl) isTopicActive(folder, topic string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	_, active := q.activeTopics[topicKey{folder: folder, topic: topic}]
	return active
}

func (q *turnControl) activateLocked(jid string) {
	q.getGroup(jid).active = true
}

func (q *turnControl) canReserve(r turnReservation) bool {
	_, busy := q.activeTopics[r.key()]
	return !busy && q.activeCount < q.maxConcurrent && (!r.cron || q.activeCron == 0)
}

func (q *turnControl) reserveLocked(jid string, r turnReservation) {
	q.reservations[jid] = r
	q.activeCount++
	if r.cron {
		q.activeCron++
	}
	q.activeTopics[r.key()] = jid
}

func (q *turnControl) reserve(jid, folder, topic string, cron bool, turnID string) bool {
	q.mu.Lock()
	r := turnReservation{folder: folder, topic: topic, cron: cron, turnID: turnID}
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
	delete(q.activeTopics, r.key())
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
		s.again = true
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

	q.mu.Lock()
	s := q.getGroup(groupJid)
	s.active = false
	again := s.again
	s.again = false
	var decision retry.Decision
	if err == nil {
		s.processFailures = 0
	} else {
		now := q.now()
		failure := retry.FromError(err, now)
		decision = retry.Next(s.processFailures, failure, now, time.Time{})
		s.processFailures = decision.Failures
	}
	q.releaseLocked(groupJid)
	shutdown := q.shuttingDown
	if !shutdown {
		q.drainWaitingLocked()
	}
	q.mu.Unlock()
	if err != nil {
		slog.Error("error processing messages for group", "groupJid", groupJid, "err", err)
		if decision.Stop {
			slog.Error(retry.Notice("Message processing failed", decision), "groupJid", groupJid)
		}
	}
	if shutdown {
		return
	}
	if again {
		q.EnqueueMessageCheck(groupJid)
	} else if err != nil && !decision.Stop {
		q.mu.Lock()
		if !q.shuttingDown {
			time.AfterFunc(max(0, decision.NextAt.Sub(q.now())), func() { q.EnqueueMessageCheck(groupJid) })
		}
		q.mu.Unlock()
	}
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
