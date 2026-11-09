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
	"github.com/kronael/arizuko/store"
)

const deferredWorkPrefix = "\x00turn:"

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
			ok, err := l.db.store().MigrateDeferredTurn(context.Background(), store.DeferredTurn{Turn: tc, PriorCursor: cursor})
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
		ok, err := l.db.store().MigrateDeferredTurn(context.Background(), store.DeferredTurn{Turn: tc, PriorCursor: cursor})
		if err != nil || !ok {
			return fmt.Errorf("migrate turn %s: binding refused: %v", tc.TurnID, err)
		}
	}
	return nil
}

func (l *Loop) isSavedExecution(tc TurnContext) bool {
	return len(tc.InputKeys) > 0 && tc.InputLast != "" && tc.InputLast <= l.db.GetAgentCursor(tc.ChatJID)
}

func (q *turnControl) recover(l *Loop) error {
	turns, err := l.db.RunningTurns()
	if err != nil {
		return err
	}
	for _, tc := range turns {
		if tc.State == store.TurnPendingRetry && tc.NextAttemptAt.IsZero() {
			if err := l.migrateRetries(tc.ChatJID); err != nil {
				return err
			}
		}
		if tc.State != store.TurnRunning || !l.isSavedExecution(tc) {
			continue
		}
		_, err := q.WithIdleFolder(tc.Folder, func() error {
			if err := l.db.SetRunReturned(tc.TurnID); err != nil {
				return err
			}
			if err := l.stopModelProbe(tc); err != nil {
				return err
			}
			if tc.RetryCount >= l.maxTurnRetry {
				finishTurn(l.db, l.deliver, tc.TurnID, endFailed)
				return nil
			}
			tc.NextAttemptAt = retryAt(q.now(), tc.Trigger, tc.RetryCount+1)
			_, err := l.db.store().DeferTurn(context.Background(), store.DeferredTurn{
				Turn: tc, PriorCursor: l.db.GetAgentCursor(tc.ChatJID)})
			return err
		})
		if err != nil {
			return err
		}
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
