package queue

import (
	"errors"
	"log/slog"
	"slices"
	"sync"
)

const circuitBreakerThreshold = 3

// ErrDependencyDown marks a failure the GROUP did not cause: the execution
// plane was never reached, so no work was attempted on this group's behalf.
// The caller wraps its transport error with it; the breaker then leaves the
// group's streak alone and the batch re-feeds on the next poll.
//
// Without it one runed restart is charged to every group at once — a krons
// deploy opened four groups' breakers on `connect: connection refused` to
// runed:8080 before runed had finished starting (BUGS C9). "runed is not up"
// and "this group's turns keep failing" are different signals and only the
// second one is the breaker's business.
var ErrDependencyDown = errors.New("execution plane unreachable")

type groupState struct {
	active              bool
	consecutiveFailures int
}

type processMessagesFn func(groupJid string) (bool, error)
type folderForJidFn func(groupJid string) string

type GroupQueue struct {
	mu              sync.Mutex
	groups          map[string]*groupState
	activeCount     int
	activeFolders   map[string]string
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	folderForJid    folderForJidFn
	shuttingDown    bool
}

func New(maxConcurrent int) *GroupQueue {
	return &GroupQueue{
		groups:        make(map[string]*groupState),
		activeFolders: make(map[string]string),
		maxConcurrent: maxConcurrent,
	}
}

func (q *GroupQueue) getGroup(groupJid string) *groupState {
	s := q.groups[groupJid]
	if s == nil {
		s = &groupState{}
		q.groups[groupJid] = s
	}
	return s
}

func (q *GroupQueue) SetProcessMessagesFn(fn processMessagesFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.processMessages = fn
}
func (q *GroupQueue) SetFolderForJidFn(fn folderForJidFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.folderForJid = fn
}

// Caller must hold q.mu.
func (q *GroupQueue) folderOf(jid string) string {
	if q.folderForJid == nil {
		return ""
	}
	return q.folderForJid(jid)
}

// Caller must hold q.mu.
func (q *GroupQueue) activateLocked(jid, folder string) {
	s := q.getGroup(jid)
	s.active = true
	q.activeCount++
	if folder != "" {
		q.activeFolders[folder] = jid
	}
}

func (q *GroupQueue) EnqueueMessageCheck(groupJid string) {
	q.mu.Lock()
	if q.shuttingDown {
		q.mu.Unlock()
		return
	}

	s := q.getGroup(groupJid)

	if s.consecutiveFailures >= circuitBreakerThreshold {
		slog.Info("circuit breaker reset by new message",
			"groupJid", groupJid, "failures", s.consecutiveFailures)
		s.consecutiveFailures = 0
	}

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
			q.mu.Unlock()
			return
		}
	}

	if q.activeCount >= q.maxConcurrent {
		if !slices.Contains(q.waitingGroups, groupJid) {
			q.waitingGroups = append(q.waitingGroups, groupJid)
		}
		slog.Debug("at concurrency limit, queued for drain",
			"groupJid", groupJid, "activeCount", q.activeCount)
		q.mu.Unlock()
		return
	}

	q.activateLocked(groupJid, folder)
	q.mu.Unlock()

	go q.runForGroup(groupJid)
}

// Shutdown stops admitting work. A live container is runed's to reap: the
// queue paces dispatch and holds no container handle.
func (q *GroupQueue) Shutdown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	slog.Info("GroupQueue shutting down (containers detached, not killed)",
		"activeCount", q.activeCount)
}

func (q *GroupQueue) runForGroup(groupJid string) {
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
		// A clean return is success even with no output: silent no-ops (observe,
		// already-current cursor, agent chose not to reply) advance the cursor
		// and never re-feed, so they are progress, not failure. Counting
		// no-output as failure false-tripped the breaker after silent turns —
		// discord channel chatter on sloth+marinade, 9ms after a 0-exit run.
		s.consecutiveFailures = 0
	case errors.Is(err, ErrDependencyDown):
		// Loud, because an unreachable execution plane is an operator's problem —
		// but neither success nor this group's failure, so the streak is left
		// exactly as it was. Nothing ran, so there is nothing to credit or blame.
		slog.Error("execution plane unreachable; group work re-feeds on next poll",
			"groupJid", groupJid, "failures", s.consecutiveFailures, "err", err)
	default:
		// Only a failure attributable to processing THIS group counts.
		s.consecutiveFailures++
		slog.Error("error processing messages for group",
			"groupJid", groupJid, "failures", s.consecutiveFailures, "err", err)
		if s.consecutiveFailures >= circuitBreakerThreshold {
			slog.Error("circuit breaker open - too many consecutive failures",
				"groupJid", groupJid, "failures", s.consecutiveFailures)
		}
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

func (q *GroupQueue) drainWaitingLocked() {
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

func (q *GroupQueue) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeCount
}
