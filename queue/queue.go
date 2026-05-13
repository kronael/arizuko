package queue

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/groupfolder"
)

const circuitBreakerThreshold = 3

type groupState struct {
	active              bool
	containerName       string
	groupFolder         string
	consecutiveFailures int
}

type processMessagesFn func(groupJid string) (bool, error)
type hasPendingFn func(groupJid string) bool
type notifyErrorFn func(groupJid string, err error)
type folderForJidFn func(groupJid string) string

type GroupQueue struct {
	mu              sync.Mutex
	groups          map[string]*groupState
	activeCount     int
	activeFolders   map[string]string
	maxConcurrent   int
	waitingGroups   []string
	processMessages processMessagesFn
	hasPending      hasPendingFn
	notifyError     notifyErrorFn
	folderForJid    folderForJidFn
	shuttingDown    bool
	ipcDir          string
	// signalContainer triggers SIGUSR1 on a running container by name.
	// Returns nil on success; non-nil if the container is not running
	// (used to detect steer-into-dying-container race in SendMessages).
	signalContainer func(name string) error
}

func New(maxConcurrent int, ipcDir string) *GroupQueue {
	return &GroupQueue{
		groups:          make(map[string]*groupState),
		activeFolders:   make(map[string]string),
		maxConcurrent:   maxConcurrent,
		ipcDir:          ipcDir,
		signalContainer: defaultSignalContainer,
	}
}

func defaultSignalContainer(name string) error {
	return exec.Command(container.Bin, "kill", "--signal=SIGUSR1", name).Run()
}

// SetSignalContainerForTest overrides the SIGUSR1 sender. Tests use this
// to simulate the race (returns error → slot marked inactive) or the
// happy path (returns nil → steer succeeds) without a live docker.
func (q *GroupQueue) SetSignalContainerForTest(fn func(name string) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.signalContainer = fn
}

func (q *GroupQueue) getGroup(groupJid string) *groupState {
	s := q.groups[groupJid]
	if s == nil {
		s = &groupState{}
		q.groups[groupJid] = s
	}
	return s
}

func (q *GroupQueue) SetProcessMessagesFn(fn processMessagesFn) { q.mu.Lock(); defer q.mu.Unlock(); q.processMessages = fn }
func (q *GroupQueue) SetHasPendingFn(fn hasPendingFn)           { q.mu.Lock(); defer q.mu.Unlock(); q.hasPending = fn }
func (q *GroupQueue) SetNotifyErrorFn(fn notifyErrorFn)         { q.mu.Lock(); defer q.mu.Unlock(); q.notifyError = fn }
func (q *GroupQueue) SetFolderForJidFn(fn folderForJidFn)       { q.mu.Lock(); defer q.mu.Unlock(); q.folderForJid = fn }

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

func (q *GroupQueue) RegisterProcess(groupJid, containerName, groupFolder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.getGroup(groupJid)
	s.containerName = containerName
	if groupFolder != "" {
		s.groupFolder = groupFolder
	}
}

// SetActiveForTest simulates an active container without running docker.
// Also installs a no-op signalContainer so SendMessages doesn't try to
// SIGUSR1 a non-existent container; tests that want to simulate the
// race override afterwards via SetSignalContainerForTest.
func (q *GroupQueue) SetActiveForTest(groupJid, containerName, groupFolder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := q.getGroup(groupJid)
	s.active = true
	s.containerName = containerName
	s.groupFolder = groupFolder
	q.activeCount++
	q.signalContainer = func(string) error { return nil }
}

func (q *GroupQueue) SendMessages(groupJid string, texts []string) bool {
	if len(texts) == 0 {
		return false
	}
	q.mu.Lock()
	s := q.getGroup(groupJid)
	if !s.active || s.groupFolder == "" {
		q.mu.Unlock()
		return false
	}
	folder := s.groupFolder
	cname := s.containerName
	q.mu.Unlock()

	ipcFolder := filepath.Join(q.ipcDir, folder)
	written := 0
	for _, text := range texts {
		if err := writeIpcFile(ipcFolder, text); err != nil {
			slog.Warn("steer: ipc write failed",
				"jid", groupJid, "folder", folder, "err", err)
			continue
		}
		written++
	}
	if written == 0 {
		return false
	}
	if err := q.signalContainer(cname); err != nil {
		// Race: ant runner exited (no input → graceful shutdown) but the
		// docker container hasn't fully reported "exited" yet, so the
		// queue slot still looks active. The IPC files we just wrote are
		// orphaned — they'll be drained by the next spawn via
		// drainIpcInput() at session start (ant/src/index.ts). Mark the
		// slot inactive so the caller falls through to EnqueueMessageCheck
		// and we get a fresh container right away instead of waiting for
		// the next inbound message.
		q.mu.Lock()
		s.active = false
		s.containerName = ""
		if q.activeFolders[folder] == groupJid {
			delete(q.activeFolders, folder)
		}
		if q.activeCount > 0 {
			q.activeCount--
		}
		q.mu.Unlock()
		slog.Warn("steer: signal failed, container gone — slot marked inactive, IPC file persists for next spawn",
			"jid", groupJid, "folder", folder, "container", cname, "err", err)
		return false
	}
	slog.Info("steer: sent messages into running container",
		"jid", groupJid, "folder", folder, "count", written)
	return true
}

func writeIpcFile(ipcFolder, text string) error {
	inputDir := groupfolder.IpcInputDir(ipcFolder)
	if err := os.MkdirAll(inputDir, 0o755); err != nil {
		return err
	}
	ts := time.Now().UnixMilli()
	r := rand.IntN(1679616) // 36^4
	name := fmt.Sprintf("%d-%04s.json", ts, strconv.FormatInt(int64(r), 36))
	fp := filepath.Join(inputDir, name)
	tmp := fp + ".tmp"
	payload, _ := json.Marshal(map[string]string{"type": "message", "text": text})
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, fp); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func (q *GroupQueue) Shutdown() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.shuttingDown = true
	var detached []string
	for _, s := range q.groups {
		if s.active && s.containerName != "" {
			detached = append(detached, s.containerName)
		}
	}
	slog.Info("GroupQueue shutting down (containers detached, not killed)",
		"activeCount", q.activeCount, "detachedContainers", detached)
}

func (q *GroupQueue) runForGroup(groupJid string) {
	slog.Debug("starting container for group", "groupJid", groupJid, "activeCount", q.activeCount)

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
		slog.Error("error processing messages for group", "groupJid", groupJid, "err", err)
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
	for f, owner := range q.activeFolders {
		if owner == groupJid {
			delete(q.activeFolders, f)
			break
		}
	}
	if !q.shuttingDown && !q.startGroupLocked(groupJid) {
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

func (q *GroupQueue) startGroupLocked(jid string) bool {
	if q.hasPending == nil || !q.hasPending(jid) {
		return false
	}
	folder := q.folderOf(jid)
	if folder != "" {
		if other, busy := q.activeFolders[folder]; busy && other != jid {
			return false
		}
	}
	q.activateLocked(jid, folder)
	go q.runForGroup(jid)
	return true
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

	return exec.Command(container.Bin, container.StopContainerArgs(name)...).Run() == nil
}
