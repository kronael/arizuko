package queue

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/onvos/arizuko/container"
)

const circuitBreakerThreshold = 3

type taskFn func() error

type queuedTask struct {
	ID string
	Fn taskFn
}

type groupState struct {
	active              bool
	idleWaiting         bool
	isTaskContainer     bool
	pendingMessages     bool
	pendingTasks        []queuedTask
	containerName       string
	groupFolder         string
	consecutiveFailures int
}

type processMessagesFn func(groupJid string) (bool, error)
type notifyErrorFn func(groupJid string, err error)

type GroupQueue struct {
	mu              sync.Mutex
	groups          map[string]*groupState
	activeCount     int
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	notifyError     notifyErrorFn
	shuttingDown    bool
	dataDir         string
}

func New(maxConcurrent int, dataDir string) *GroupQueue {
	return &GroupQueue{
		groups:        make(map[string]*groupState),
		maxConcurrent: maxConcurrent,
		dataDir:       dataDir,
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

func (q *GroupQueue) SetNotifyErrorFn(fn notifyErrorFn) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.notifyError = fn
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
		s.pendingMessages = true
		slog.Debug("container active, message queued", "groupJid", groupJid)
		q.mu.Unlock()
		return
	}

	if q.activeCount >= q.maxConcurrent {
		s.pendingMessages = true
		if !q.hasWaiting(groupJid) {
			q.waitingGroups = append(q.waitingGroups, groupJid)
		}
		slog.Debug("at concurrency limit, message queued",
			"groupJid", groupJid, "activeCount", q.activeCount)
		q.mu.Unlock()
		return
	}

	s.active = true
	s.idleWaiting = false
	s.isTaskContainer = false
	s.pendingMessages = false
	q.activeCount++
	q.mu.Unlock()

	go q.runForGroup(groupJid, "messages")
}

func (q *GroupQueue) EnqueueTask(groupJid, taskID string, fn taskFn) {
	q.mu.Lock()
	if q.shuttingDown {
		q.mu.Unlock()
		return
	}

	s := q.getGroup(groupJid)

	for _, t := range s.pendingTasks {
		if t.ID == taskID {
			slog.Debug("task already queued, skipping",
				"groupJid", groupJid, "taskId", taskID)
			q.mu.Unlock()
			return
		}
	}

	task := queuedTask{ID: taskID, Fn: fn}

	if s.active {
		s.pendingTasks = append(s.pendingTasks, task)
		if s.idleWaiting {
			q.closeStdinLocked(s)
		}
		slog.Debug("container active, task queued",
			"groupJid", groupJid, "taskId", taskID)
		q.mu.Unlock()
		return
	}

	if q.activeCount >= q.maxConcurrent {
		s.pendingTasks = append(s.pendingTasks, task)
		if !q.hasWaiting(groupJid) {
			q.waitingGroups = append(q.waitingGroups, groupJid)
		}
		slog.Debug("at concurrency limit, task queued",
			"groupJid", groupJid, "taskId", taskID, "activeCount", q.activeCount)
		q.mu.Unlock()
		return
	}

	s.active = true
	s.idleWaiting = false
	s.isTaskContainer = true
	q.activeCount++
	q.mu.Unlock()

	go q.runTask(groupJid, task)
}

func (q *GroupQueue) RegisterProcess(groupJid, containerName, groupFolder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.getGroup(groupJid)
	s.containerName = containerName
	if groupFolder != "" {
		s.groupFolder = groupFolder
	}
}

func (q *GroupQueue) SendMessage(groupJid, text string) bool {
	q.mu.Lock()
	s := q.getGroup(groupJid)
	if !s.active || s.groupFolder == "" || s.isTaskContainer {
		q.mu.Unlock()
		return false
	}
	s.idleWaiting = false
	folder := s.groupFolder
	container := s.containerName
	q.mu.Unlock()

	inputDir := filepath.Join(q.dataDir, "ipc", folder, "input")
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return false
	}

	ts := time.Now().UnixMilli()
	r := rand.IntN(1679616) // 36^4
	name := fmt.Sprintf("%d-%s.json", ts, base36(r))
	fp := filepath.Join(inputDir, name)
	tmp := fp + ".tmp"

	payload, _ := json.Marshal(map[string]string{"type": "message", "text": text})
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return false
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return false
	}

	if container != "" {
		signalContainer(container)
	}
	return true
}

func (q *GroupQueue) closeStdinLocked(s *groupState) {
	inputDir := filepath.Join(q.dataDir, "ipc", s.groupFolder, "input")
	_ = os.MkdirAll(inputDir, 0o755)
	_ = os.WriteFile(filepath.Join(inputDir, "_close"), nil, 0o644)
	if s.containerName != "" {
		signalContainer(s.containerName)
	}
}

func (q *GroupQueue) Shutdown() {
	q.mu.Lock()
	q.shuttingDown = true

	var detached []string
	for _, s := range q.groups {
		if s.active && s.containerName != "" {
			detached = append(detached, s.containerName)
		}
	}
	q.mu.Unlock()

	slog.Info("GroupQueue shutting down (containers detached, not killed)",
		"activeCount", q.activeCount, "detachedContainers", detached)
}

func (q *GroupQueue) runForGroup(groupJid, reason string) {
	slog.Debug("starting container for group",
		"groupJid", groupJid, "reason", reason, "activeCount", q.activeCount)

	q.mu.Lock()
	fn := q.processMessages
	q.mu.Unlock()

	var success bool
	var err error
	if fn != nil {
		success, err = fn(groupJid)
	}

	q.mu.Lock()
	s := q.getGroup(groupJid)
	notifyFn := q.notifyError
	if err != nil {
		s.consecutiveFailures++
		slog.Error("error processing messages for group",
			"groupJid", groupJid, "err", err)
		if notifyFn != nil {
			go notifyFn(groupJid, err)
		}
	} else if success {
		s.consecutiveFailures = 0
	} else {
		s.consecutiveFailures++
		if s.consecutiveFailures >= circuitBreakerThreshold {
			slog.Error("circuit breaker open - too many consecutive failures",
				"groupJid", groupJid, "failures", s.consecutiveFailures)
			if notifyFn != nil {
				go notifyFn(groupJid, fmt.Errorf("too many failures, send another message to retry"))
			}
		}
	}

	s.active = false
	s.containerName = ""
	s.groupFolder = ""
	q.activeCount--
	q.drainGroupLocked(groupJid)
	q.mu.Unlock()
}

func (q *GroupQueue) runTask(groupJid string, task queuedTask) {
	slog.Debug("running queued task",
		"groupJid", groupJid, "taskId", task.ID, "activeCount", q.activeCount)

	err := task.Fn()
	if err != nil {
		slog.Error("error running task",
			"groupJid", groupJid, "taskId", task.ID, "err", err)
	}

	q.mu.Lock()
	s := q.getGroup(groupJid)
	s.active = false
	s.isTaskContainer = false
	s.containerName = ""
	s.groupFolder = ""
	q.activeCount--
	q.drainGroupLocked(groupJid)
	q.mu.Unlock()
}

// startGroupLocked picks and launches the next pending work for jid.
// Returns true if something was started. Must be called with q.mu held.
func (q *GroupQueue) startGroupLocked(jid string) bool {
	s := q.getGroup(jid)
	if len(s.pendingTasks) > 0 {
		task := s.pendingTasks[0]
		s.pendingTasks = s.pendingTasks[1:]
		s.active, s.idleWaiting, s.isTaskContainer = true, false, true
		q.activeCount++
		go q.runTask(jid, task)
		return true
	}
	if s.pendingMessages {
		s.active, s.idleWaiting, s.isTaskContainer = true, false, false
		s.pendingMessages = false
		q.activeCount++
		go q.runForGroup(jid, "drain")
		return true
	}
	return false
}

func (q *GroupQueue) drainGroupLocked(groupJid string) {
	if q.shuttingDown {
		return
	}
	if !q.startGroupLocked(groupJid) {
		q.drainWaitingLocked()
	}
}

func (q *GroupQueue) drainWaitingLocked() {
	for len(q.waitingGroups) > 0 && q.activeCount < q.maxConcurrent {
		jid := q.waitingGroups[0]
		q.waitingGroups = q.waitingGroups[1:]
		q.startGroupLocked(jid)
	}
}

func (q *GroupQueue) hasWaiting(groupJid string) bool {
	for _, jid := range q.waitingGroups {
		if jid == groupJid {
			return true
		}
	}
	return false
}

func signalContainer(name string) {
	_ = exec.Command(container.Bin, "kill", "--signal=SIGUSR1", name).Run()
}

func (q *GroupQueue) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.activeCount
}

func (q *GroupQueue) StopProcess(jid string) bool {
	q.mu.Lock()
	s := q.groups[jid]
	if s == nil || !s.active || s.containerName == "" {
		q.mu.Unlock()
		return false
	}
	name := s.containerName
	q.mu.Unlock()

	cmd := exec.Command(container.Bin, container.StopContainerArgs(name)...)
	return cmd.Run() == nil
}

func base36(n int) string {
	const chars = "0123456789abcdefghijklmnopqrstuvwxyz"
	if n == 0 {
		return "0000"
	}
	b := make([]byte, 4)
	for i := 3; i >= 0; i-- {
		b[i] = chars[n%36]
		n /= 36
	}
	return string(b)
}
