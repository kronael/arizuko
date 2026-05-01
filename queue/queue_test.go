package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewQueue(t *testing.T) {
	q := New(3, t.TempDir())
	if q.maxConcurrent != 3 {
		t.Fatalf("expected maxConcurrent 3, got %d", q.maxConcurrent)
	}
	if q.ActiveCount() != 0 {
		t.Fatal("expected 0 active")
	}
}

func TestEnqueueMessageCheckStartsContainer(t *testing.T) {
	q := New(5, t.TempDir())

	var called atomic.Bool
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if !called.Load() {
		t.Fatal("processMessages not called")
	}
}

func TestEnqueueMessageCheckQueuesWhenActive(t *testing.T) {
	q := New(5, t.TempDir())

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		n := calls.Add(1)
		if n == 1 {
			started <- struct{}{}
			<-block // only first call blocks
		}
		return true, nil
	})
	// DB says there are pending messages — drain should re-process
	q.SetHasPendingFn(func(jid string) bool { return true })

	q.EnqueueMessageCheck("g1")
	<-started

	// Second enqueue while active — no flag, but hasPending will trigger drain
	q.EnqueueMessageCheck("g1")

	close(block)
	time.Sleep(100 * time.Millisecond)

	if calls.Load() < 2 {
		t.Fatalf("expected >= 2 calls (initial + drain), got %d", calls.Load())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	q := New(1, t.TempDir())

	started := make(chan struct{})
	block := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-block
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	<-started

	// g2 should be queued, not started
	q.EnqueueMessageCheck("g2")
	time.Sleep(50 * time.Millisecond)

	if q.ActiveCount() != 1 {
		t.Fatalf("expected 1 active, got %d", q.ActiveCount())
	}

	q.mu.Lock()
	waiting := len(q.waitingGroups)
	q.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("expected 1 waiting, got %d", waiting)
	}

	close(block)
	time.Sleep(100 * time.Millisecond)
}

func TestCircuitBreaker(t *testing.T) {
	q := New(5, t.TempDir())

	var calls atomic.Int32
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		calls.Add(1)
		return false, nil // failure
	})

	// 3 failures to trip breaker
	for i := 0; i < 3; i++ {
		q.EnqueueMessageCheck("g1")
		time.Sleep(50 * time.Millisecond)
	}

	q.mu.Lock()
	failures := q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	if failures < circuitBreakerThreshold {
		t.Fatalf("expected >= %d failures, got %d", circuitBreakerThreshold, failures)
	}

	// New message resets breaker
	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)

	q.mu.Lock()
	failures = q.groups["g1"].consecutiveFailures
	q.mu.Unlock()
	// After reset + another failure, should be 1
	if failures >= circuitBreakerThreshold {
		t.Fatal("circuit breaker should have been reset")
	}
}

func TestShutdownBlocksEnqueue(t *testing.T) {
	q := New(5, t.TempDir())
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		return true, nil
	})

	q.Shutdown()

	var called atomic.Bool
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if called.Load() {
		t.Fatal("should not process after shutdown")
	}
}

// SendMessages drives mid-loop steering: writes one IPC file per
// message and signals the container. Must be a no-op when no container
// is active for the group.

func TestSendMessages_EmptyInputReturnsFalse(t *testing.T) {
	q := New(1, t.TempDir())
	if q.SendMessages("g1", nil) {
		t.Fatal("empty input should return false")
	}
	if q.SendMessages("g1", []string{}) {
		t.Fatal("zero-length slice should return false")
	}
}

func TestSendMessages_NoActiveContainerReturnsFalse(t *testing.T) {
	q := New(1, t.TempDir())
	// No RegisterProcess → active=false
	if q.SendMessages("g1", []string{"hi"}) {
		t.Fatal("steer should fail when no container is active")
	}
}

func TestSendMessages_NoGroupFolderReturnsFalse(t *testing.T) {
	q := New(1, t.TempDir())
	// Active but groupFolder still empty (unregistered).
	q.mu.Lock()
	s := q.getGroup("g1")
	s.active = true
	q.mu.Unlock()

	if q.SendMessages("g1", []string{"hi"}) {
		t.Fatal("steer should fail with empty groupFolder")
	}
}

func TestSendMessages_WritesOneFilePerMessage(t *testing.T) {
	ipcDir := t.TempDir()
	q := New(1, ipcDir)

	q.mu.Lock()
	s := q.getGroup("g1")
	s.active = true
	s.groupFolder = "fold"
	s.containerName = "fake-container-name-that-wont-exist"
	q.mu.Unlock()

	texts := []string{"first", "second", "third"}
	ok := q.SendMessages("g1", texts)
	if !ok {
		t.Fatal("SendMessages returned false, want true")
	}

	inputDir := filepath.Join(ipcDir, "fold", "input")
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		t.Fatalf("read input dir: %v", err)
	}
	jsonFiles := 0
	seen := map[string]bool{}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			jsonFiles++
			data, err := os.ReadFile(filepath.Join(inputDir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			var payload map[string]string
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("unmarshal %s: %v", e.Name(), err)
			}
			if payload["type"] != "message" {
				t.Errorf("type = %q, want 'message'", payload["type"])
			}
			seen[payload["text"]] = true
		}
	}
	if jsonFiles != len(texts) {
		t.Errorf("json files = %d, want %d", jsonFiles, len(texts))
	}
	for _, text := range texts {
		if !seen[text] {
			t.Errorf("missing file with text=%q", text)
		}
	}
}

// Two JIDs that map to the same folder must serialize: only one container
// runs at a time, the other waits and starts after the first finishes.
// Regression: at startup, recoverPendingMessages and checkMigrationVersion
// would enqueue different JIDs (telegram:..., atlas) for the same
// folder, spawning two parallel containers that double-narrated logs.
func TestEnqueueSerializesByFolder(t *testing.T) {
	q := New(5, t.TempDir())
	q.SetFolderForJidFn(func(jid string) string {
		switch jid {
		case "telegram:atlas", "atlas":
			return "atlas"
		}
		return ""
	})

	// hasPending=false so each finishing run does NOT self-restart;
	// drainWaitingLocked is what we want to exercise here.
	q.SetHasPendingFn(func(string) bool { return false })

	var concurrent atomic.Int32
	var maxSeen atomic.Int32
	seenJids := map[string]bool{}
	var smu sync.Mutex
	gate := make(chan struct{})
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		n := concurrent.Add(1)
		if n > maxSeen.Load() {
			maxSeen.Store(n)
		}
		smu.Lock()
		seenJids[jid] = true
		smu.Unlock()
		<-gate
		concurrent.Add(-1)
		return true, nil
	})

	q.EnqueueMessageCheck("telegram:atlas")
	q.EnqueueMessageCheck("atlas")

	time.Sleep(80 * time.Millisecond)
	if got := concurrent.Load(); got != 1 {
		t.Fatalf("expected 1 concurrent run for shared folder, got %d", got)
	}
	q.mu.Lock()
	waiting := len(q.waitingGroups)
	q.mu.Unlock()
	if waiting != 1 {
		t.Fatalf("expected 1 jid waiting on folder, got %d", waiting)
	}

	close(gate)
	time.Sleep(150 * time.Millisecond)

	if maxSeen.Load() != 1 {
		t.Fatalf("expected max concurrency 1, got %d", maxSeen.Load())
	}
	smu.Lock()
	defer smu.Unlock()
	if !seenJids["telegram:atlas"] || !seenJids["atlas"] {
		t.Fatalf("both JIDs must run; saw %v", seenJids)
	}
}

func TestSendMessages_NoLeftoverTmpFiles(t *testing.T) {
	ipcDir := t.TempDir()
	q := New(1, ipcDir)
	q.mu.Lock()
	s := q.getGroup("g1")
	s.active = true
	s.groupFolder = "fold"
	q.mu.Unlock()

	q.SendMessages("g1", []string{"msg"})

	entries, _ := os.ReadDir(filepath.Join(ipcDir, "fold", "input"))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file: %s", e.Name())
		}
	}
}
