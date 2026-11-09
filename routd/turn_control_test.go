package routd

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func setExecutionProcessor(q *turnControl, fn processMessagesFn) {
	q.SetProcessMessagesFn(func(jid string) (bool, error) {
		folder := q.folderOf(jid)
		if folder == "" {
			folder = jid
		}
		if !q.reserve(jid, folder, false, jid) {
			return false, nil
		}
		return fn(jid)
	})
}

func queueHasWorker(q *turnControl) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, state := range q.groups {
		if state.active {
			return true
		}
	}
	return false
}

func assertRecoveryDoesNotBlockQueue(t *testing.T) {
	t.Helper()
	q := newTurnControl(1)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	go func() { _, _ = q.WithIdleFolder("a", func() error { close(entered); <-release; return nil }) }()
	<-entered
	go func() { q.EnqueueMessageCheck("b"); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery blocked queue")
	}
}

func TestWithIdleFolderExcludesAdmission(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprintf("error=%t", failure), func(t *testing.T) {
			assertRecoveryDoesNotBlockQueue(t)
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				unblock := sync.OnceFunc(func() { close(release) })
				defer unblock()
				q := newTurnControl(1)
				q.SetFolderForJidFn(func(jid string) string { return jid })
				started := make(chan string, 4)
				setExecutionProcessor(q, func(jid string) (bool, error) { started <- jid; return true, nil })
				entered, recovered := make(chan struct{}), make(chan error, 1)
				want := errors.New("recovery failed")
				go func() {
					_, err := q.WithIdleFolder("a", func() error {
						close(entered)
						<-release
						if failure {
							return want
						}
						return nil
					})
					recovered <- err
				}()
				<-entered
				go q.EnqueueMessageCheck("a")
				go q.EnqueueMessageCheck("b")
				synctest.Wait()
				select {
				case <-release:
					t.Fatal("recovery blocked queue")
				default:
				}
				select {
				case jid := <-started:
					if jid != "b" {
						t.Fatalf("started %s during recovery", jid)
					}
				default:
					t.Fatal("other folder did not start")
				}
				duplicate := make(chan bool, 1)
				go func() { ok, _ := q.WithIdleFolder("a", func() error { return nil }); duplicate <- ok }()
				synctest.Wait()
				select {
				case ok := <-duplicate:
					if ok {
						t.Fatal("duplicate recovery started")
					}
				default:
					t.Fatal("duplicate recovery blocked")
				}
				select {
				case jid := <-started:
					t.Fatalf("started %s during recovery", jid)
				default:
				}
				unblock()
				synctest.Wait()
				err := <-recovered
				if (failure && !errors.Is(err, want)) || (!failure && err != nil) {
					t.Fatalf("recovery error: %v", err)
				}
				select {
				case jid := <-started:
					if jid != "a" {
						t.Fatalf("started %s", jid)
					}
				default:
					t.Fatal("waiting folder did not start")
				}
				select {
				case jid := <-started:
					t.Fatalf("started twice: %s", jid)
				default:
				}
			})
		})
	}
}

func TestWithIdleFolderShutdown(t *testing.T) {
	assertRecoveryDoesNotBlockQueue(t)
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		unblock := sync.OnceFunc(func() { close(release) })
		defer unblock()
		q := newTurnControl(1)
		q.SetFolderForJidFn(func(jid string) string { return jid })
		started := make(chan string, 1)
		setExecutionProcessor(q, func(jid string) (bool, error) { started <- jid; return true, nil })
		entered, recovered := make(chan struct{}), make(chan struct{})
		go func() {
			_, _ = q.WithIdleFolder("a", func() error { close(entered); <-release; return nil })
			close(recovered)
		}()
		<-entered
		go q.EnqueueMessageCheck("a")
		synctest.Wait()
		select {
		case <-release:
			t.Fatal("admission blocked")
		default:
		}
		stopped := make(chan struct{})
		go func() { q.Shutdown(); close(stopped) }()
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Fatal("shutdown blocked")
		}
		unblock()
		synctest.Wait()
		<-recovered
		select {
		case jid := <-started:
			t.Fatalf("started after shutdown: %s", jid)
		default:
		}
	})
}

func TestNewQueue(t *testing.T) {
	q := newTurnControl(3)
	if q.maxConcurrent != 3 {
		t.Fatalf("expected maxConcurrent 3, got %d", q.maxConcurrent)
	}
	if q.ActiveCount() != 0 {
		t.Fatal("expected 0 active")
	}
}

func TestEnqueueMessageCheckStartsContainer(t *testing.T) {
	q := newTurnControl(5)

	var called atomic.Bool
	setExecutionProcessor(q, func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if !called.Load() {
		t.Fatal("processMessages not called")
	}
}

// A second enqueue for a group that is already running is COLLAPSED: the
// running turn drains whatever arrived, and routd's poll re-feeds anything
// left. The queue never self-restarts a group it just finished.
func TestEnqueueMessageCheckCollapsesWhileActive(t *testing.T) {
	q := newTurnControl(5)

	var calls atomic.Int32
	started := make(chan struct{}, 1)
	block := make(chan struct{})
	setExecutionProcessor(q, func(jid string) (bool, error) {
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-block // only first call blocks
		}
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	<-started

	q.EnqueueMessageCheck("g1")
	if n := calls.Load(); n != 1 {
		t.Fatalf("enqueue while active started %d runs, want 1", n)
	}

	close(block)
	time.Sleep(100 * time.Millisecond)

	if n := calls.Load(); n != 1 {
		t.Fatalf("group ran %d times, want 1: the finished run must not restart itself", n)
	}
	if q.ActiveCount() != 0 {
		t.Fatalf("activeCount = %d after the run, want 0", q.ActiveCount())
	}
}

func TestConcurrencyLimit(t *testing.T) {
	q := newTurnControl(1)

	started := make(chan struct{})
	block := make(chan struct{})
	setExecutionProcessor(q, func(jid string) (bool, error) {
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

func TestShutdownBlocksEnqueue(t *testing.T) {
	q := newTurnControl(5)
	setExecutionProcessor(q, func(jid string) (bool, error) {
		return true, nil
	})

	q.Shutdown()

	var called atomic.Bool
	setExecutionProcessor(q, func(jid string) (bool, error) {
		called.Store(true)
		return true, nil
	})

	q.EnqueueMessageCheck("g1")
	time.Sleep(50 * time.Millisecond)
	if called.Load() {
		t.Fatal("should not process after shutdown")
	}
}

// Two JIDs that map to the same folder must serialize: only one container
// runs at a time, the other waits and starts after the first finishes.
// Regression: at startup, recoverPendingMessages and checkMigrationVersion
// would enqueue different JIDs (telegram:..., atlas) for the same
// folder, spawning two parallel containers that double-narrated logs.
func TestEnqueueSerializesByFolder(t *testing.T) {
	q := newTurnControl(5)
	q.SetFolderForJidFn(func(jid string) string {
		switch jid {
		case "telegram:atlas", "atlas":
			return "atlas"
		}
		return ""
	})

	var concurrent atomic.Int32
	var maxSeen atomic.Int32
	seenJids := map[string]bool{}
	var smu sync.Mutex
	gate := make(chan struct{})
	setExecutionProcessor(q, func(jid string) (bool, error) {
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
