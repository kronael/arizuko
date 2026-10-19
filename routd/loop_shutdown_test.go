package routd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuf is an io.Writer a slog handler and the test goroutine can share.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs routes slog to a buffer for the duration of a test.
func captureLogs(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestLoopWaitBlocksWhileRunning: Wait is what routd's main holds the DB open
// on. It must not report "stopped" while the poll loop is still running —
// otherwise the deferred db.Close() lands mid-poll, Routes() fails and
// resolve() fails FORWARD: cursor advanced, message dropped as a route-miss
// (BUGS L160).
func TestLoopWaitBlocksWhileRunning(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	defer db.Close()

	loop := NewLoop(db, &stubRunner{t: t}, LoopConfig{PollEvery: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go loop.Run(ctx)

	if loop.Wait(50 * time.Millisecond) {
		t.Fatal("Wait reported stopped while Run is live — the DB would be closed under a poll")
	}

	cancel()
	if !loop.Wait(5 * time.Second) {
		t.Fatal("Wait did not observe Run returning after cancel; shutdown would hang")
	}
}

// TestLoopShutdownDropsNoMessage: the whole point of the wait. Close the DB
// only after Wait reports the loop stopped, and the route-miss drop resolve()
// logs on a failed Routes() read never fires. sloth logged exactly that 22 ms
// after "routd stopping" (BUGS L160).
func TestLoopShutdownDropsNoMessage(t *testing.T) {
	logs := captureLogs(t)

	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	loop := NewLoop(db, &stubRunner{t: t}, LoopConfig{PollEvery: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)

	// Let the loop poll a few times, then shut down in routd main's order.
	time.Sleep(20 * time.Millisecond)
	cancel()
	if !loop.Wait(5 * time.Second) {
		t.Fatal("poll loop did not stop")
	}
	db.Close()

	// Nothing may touch the DB after this point; give a stray poll room to log.
	time.Sleep(20 * time.Millisecond)

	if got := logs.String(); strings.Contains(got, "message dropped") ||
		strings.Contains(got, "database is closed") {
		t.Fatalf("shutdown logged a route-miss drop:\n%s", got)
	}
}
