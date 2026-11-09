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

func (q *turnControl) admit(l *Loop, tc TurnContext, admission *store.TurnAdmission, saved *TurnContext) (turnWork, bool, error) {
	ctx := context.Background()
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
		var pause *store.ModelPause
		if ok {
			pause, err = l.db.store().ReadModelPause(ctx, owner.Folder)
			if err != nil {
				return work, false, err
			}
		}
		if ok && owner.State == store.TurnPendingRetry && l.db.GroupExists(owner.Folder) && pause == nil {
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
		pause, err := l.db.store().ReadModelPause(ctx, tc.Folder)
		if err != nil || pause != nil {
			return work, false, err
		}
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
		probe, err := l.modelAdmission(folder, msg)
		if errors.Is(err, store.ErrModelPaused) || errors.Is(err, errModelInputOrder) {
			return nil
		}
		if err != nil {
			return err
		}
		pause, err := l.db.store().ReadModelPause(ctx, folder)
		if err != nil {
			return err
		}
		if pause != nil && probe == nil {
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
	return turnDecision{trip: true, cause: "Agent could not complete the request",
		retry: !out.Terminal && !replied && (result.Status == "pending" || result.Status == "")}
}

func (q *turnControl) complete(l *Loop, tc TurnContext, out runedv1.RunOutcome, runErr error, work turnWork) (bool, error) {
	result, err := l.db.store().GetTurnResult(tc.Folder, tc.TurnID)
	if err != nil {
		return false, err
	}
	decision := classifyTurn(out, runErr, result, l.db.TurnHasBotReply(returnTarget(tc, tc.ChatJID), tc.TurnID))
	retry := decision.retry && tc.State == store.TurnRunning && tc.RetryCount < l.maxTurnRetry
	tc.NextAttemptAt = retryAt(q.now(), tc.Trigger, tc.RetryCount+1)
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
		owned := before.ProbeTurnID == tc.TurnID && before.ProbeAttemptID == tc.AttemptID
		if decision.trip {
			next := before
			next.Episode = randHex(16)
			if !exists || before.Phase == "closed" || owned {
				next.Phase, next.ProbeTurnID, next.ProbeAttemptID = "open", "", ""
				next.NextProbeAt = q.now().Add(time.Hour)
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
		} else if after != nil && tc.State == store.TurnRunning {
			ok, err = l.db.store().FinishControlledTurn(context.Background(), tc, prior, after)
		} else if after != nil {
			ok, err = l.db.store().WriteTurnControl(context.Background(), prior, *after)
		} else {
			return false, nil
		}
		if err != nil || ok {
			return retry && ok, err
		}
		current, ok := l.db.GetTurnContext(tc.TurnID)
		if !ok || current.State != tc.State || current.AttemptID != tc.AttemptID {
			return false, fmt.Errorf("complete turn %s refused", tc.TurnID)
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

func (l *Loop) migrateRetries(chatJID string) error {
	control, exists, err := l.db.store().ReadTurnControl(context.Background())
	if err != nil {
		return err
	}
	var before *store.TurnControlState
	if exists {
		before = &control
	}
	turns, err := l.db.RunningTurns()
	if err != nil {
		return err
	}
	for _, tc := range turns {
		if tc.ChatJID != chatJID || tc.State != store.TurnPendingRetry || !tc.NextAttemptAt.IsZero() {
			continue
		}
		cursor := l.db.GetAgentCursor(chatJID)
		if len(tc.InputKeys) > 0 {
			tc.NextAttemptAt = retryAt(l.q.now(), tc.Trigger, tc.RetryCount)
			ok, err := l.db.store().MigrateDeferredTurn(context.Background(), store.DeferredTurn{Turn: tc, PriorCursor: cursor, ControlBefore: before})
			if err != nil || !ok {
				return fmt.Errorf("migrate turn %s: saved binding refused: %v", tc.TurnID, err)
			}
			continue
		}
		msgs, _, _, err := l.pendingBatch(chatJID, cursor, pollBatch)
		if err != nil {
			return err
		}
		end := slices.IndexFunc(msgs, func(m core.Message) bool { return m.ID == tc.TurnID })
		if end < 0 {
			return fmt.Errorf("migrate turn %s: input boundary is missing", tc.TurnID)
		}
		msgs = msgs[:end+1]
		batch := l.resolveBatch(chatJID, msgs)
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
		ok, err := l.db.store().MigrateDeferredTurn(context.Background(), store.DeferredTurn{Turn: tc, PriorCursor: cursor, ControlBefore: before})
		if err != nil || !ok {
			return fmt.Errorf("migrate turn %s: binding refused: %v", tc.TurnID, err)
		}
	}
	return nil
}

func (l *Loop) isSavedExecution(tc TurnContext) bool {
	return len(tc.InputKeys) > 0 && tc.InputLast != "" && tc.InputLast <= l.db.GetAgentCursor(tc.ChatJID)
}

func (q *turnControl) recover(l *Loop, startup bool) error {
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
			if err := l.migrateRetries(tc.ChatJID); err != nil {
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
			if tc.State == store.TurnDone || tc.ProviderFailureAttempt == tc.AttemptID || tc.State == store.TurnHeld {
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

// dependencyDownGrace is how long the execution plane may be unreachable before
// the queue calls it an outage rather than a restart. A deploy restarts routd
// before runed listens, so the first polls always fail and the work re-feeds:
// 26 ERROR lines in one krons restart, which trains the reader to filter the
// line that would carry a real outage (BUGS L161). The grace cannot be a
// FAILURE COUNT — every group fails in the same poll, so a fleet of 26 crosses
// any count on the first one.
const dependencyDownGrace = 60 * time.Second

// ErrDependencyDown marks a failure the GROUP did not cause: the execution
// plane was never reached, so no work was attempted on this group's behalf.
// The caller wraps its transport error with it so the queue reports the
// execution-plane outage once after the restart grace period.
var ErrDependencyDown = errors.New("execution plane unreachable")

type groupState struct {
	active bool
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
	activeFolders   map[string]string
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	folderForJid    folderForJidFn
	parked          parkedFn
	shuttingDown    bool

	// depDownSince is when the execution plane was first found unreachable in
	// the current outage (zero when it is reachable); depDownLoud records that
	// the outage was already reported at ERROR, so it is said once, not once
	// per group per poll. Queue-wide, because the plane is.
	depDownSince time.Time
	depDownLoud  bool

	// now is the clock, injectable so a test can cross dependencyDownGrace
	// without waiting a minute.
	now func() time.Time
}

func newTurnControl(maxConcurrent int) *turnControl {
	return &turnControl{
		groups:        make(map[string]*groupState),
		activeFolders: make(map[string]string),
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

// Caller must hold q.mu.
func (q *turnControl) folderOf(jid string) string {
	if q.folderForJid == nil {
		return ""
	}
	return q.folderForJid(jid)
}

// Caller must hold q.mu.
func (q *turnControl) activateLocked(jid, folder string) {
	s := q.getGroup(jid)
	s.active = true
	q.activeCount++
	if folder != "" {
		q.activeFolders[folder] = jid
	}
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

	folder := q.folderOf(groupJid)
	if folder != "" {
		if other, busy := q.activeFolders[folder]; busy && other != groupJid {
			if !slices.Contains(q.waitingGroups, groupJid) {
				q.waitingGroups = append(q.waitingGroups, groupJid)
			}
			slog.Debug("folder already active, queued",
				"groupJid", groupJid, "folder", folder, "active_jid", other)
			fn := q.parked
			q.mu.Unlock()
			if fn != nil {
				fn(groupJid)
			}
			return
		}
	}

	if q.activeCount >= q.maxConcurrent {
		if !slices.Contains(q.waitingGroups, groupJid) {
			q.waitingGroups = append(q.waitingGroups, groupJid)
		}
		slog.Debug("at concurrency limit, queued for drain",
			"groupJid", groupJid, "activeCount", q.activeCount)
		fn := q.parked
		q.mu.Unlock()
		if fn != nil {
			fn(groupJid)
		}
		return
	}

	q.activateLocked(groupJid, folder)
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

// logDependencyDown reports one unreachable-plane outcome at the level the
// OUTAGE deserves. Caller holds q.mu.
func (q *turnControl) logDependencyDown(groupJid string, err error) {
	now := q.now()
	if q.depDownSince.IsZero() {
		q.depDownSince = now
	}
	down := now.Sub(q.depDownSince)
	switch {
	case down < dependencyDownGrace:
		slog.Warn("execution plane unreachable; group work re-feeds on next poll",
			"groupJid", groupJid, "down", down, "err", err)
	case !q.depDownLoud:
		q.depDownLoud = true
		slog.Error("execution plane unreachable past the restart window; no group work is running",
			"groupJid", groupJid, "down", down, "err", err)
	default:
		slog.Warn("execution plane still unreachable",
			"groupJid", groupJid, "down", down, "err", err)
	}
}

// clearDependencyDown ends the outage: the plane answered. Caller holds q.mu.
func (q *turnControl) clearDependencyDown() {
	if q.depDownLoud {
		slog.Info("execution plane reachable again", "down", q.now().Sub(q.depDownSince))
	}
	q.depDownSince = time.Time{}
	q.depDownLoud = false
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
	switch {
	case err == nil:
		q.clearDependencyDown()
	case errors.Is(err, ErrDependencyDown):
		q.logDependencyDown(groupJid, err)
	default:
		q.clearDependencyDown()
		slog.Error("error processing messages for group",
			"groupJid", groupJid, "err", err)
	}

	s.active = false
	q.activeCount--
	for f, owner := range q.activeFolders {
		if owner == groupJid {
			delete(q.activeFolders, f)
			break
		}
	}
	if !q.shuttingDown {
		q.drainWaitingLocked()
	}
	q.mu.Unlock()
}

func (q *turnControl) drainWaitingLocked() {
	i := 0
	for i < len(q.waitingGroups) && q.activeCount < q.maxConcurrent {
		jid := q.waitingGroups[i]
		folder := q.folderOf(jid)
		if folder != "" {
			if other, busy := q.activeFolders[folder]; busy && other != jid {
				i++
				continue
			}
		}
		q.waitingGroups = append(q.waitingGroups[:i], q.waitingGroups[i+1:]...)
		q.activateLocked(jid, folder)
		go q.runForGroup(jid)
	}
}

func (q *turnControl) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeCount
}
