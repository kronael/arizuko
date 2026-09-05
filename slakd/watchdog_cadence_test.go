package main

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// logBuf is an io.Writer a slog handler and the test goroutine can share.
type logBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *logBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *logBuf) count(sub string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Count(b.buf.String(), sub)
}

func captureSlog(t *testing.T) *logBuf {
	t.Helper()
	buf := &logBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// TestWatchdog_QuietWarnsOnceNotEveryTick: a quiet workspace is a STATE. The
// watchdog says so on the edge into quiet and stays silent while it holds —
// marinade logged 1019 identical lines in 17 h, which trains the reader to
// filter the adapter that would carry a real failure (BUGS L162).
func TestWatchdog_QuietWarnsOnceNotEveryTick(t *testing.T) {
	logs := captureSlog(t)
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	b.cfg.StaleSeconds = 1
	b.cfg.WatchdogEvery = 2 * time.Millisecond
	b.cfg.StaleFailLimit = 1_000_000 // the backstop must not fire during the test
	b.lastInboundAt.Store(time.Now().Add(-time.Hour).Unix())

	ctx := t.Context()
	go b.watchdog(ctx)

	// Wait for the first warning, then let many more ticks pass.
	if !waitFor(500*time.Millisecond, func() bool {
		return logs.count("slack inbound quiet") >= 1
	}) {
		t.Fatal("watchdog never reported the quiet stream")
	}
	time.Sleep(60 * time.Millisecond) // ~30 further ticks

	if n := logs.count("slack inbound quiet"); n != 1 {
		t.Fatalf("quiet warned %d times across ~30 stale ticks, want exactly 1", n)
	}
}

// TestWatchdog_QuietReportsRecovery: the edge OUT of quiet is an event too, so
// a reader who saw the warning learns the stream came back without having to
// infer it from silence.
func TestWatchdog_QuietReportsRecovery(t *testing.T) {
	logs := captureSlog(t)
	mock := newSlackMock()
	defer mock.Close()
	b, _ := setupBot(t, mock)
	b.cfg.StaleSeconds = 1
	b.cfg.WatchdogEvery = 2 * time.Millisecond
	b.cfg.StaleFailLimit = 1_000_000
	b.lastInboundAt.Store(time.Now().Add(-time.Hour).Unix())

	ctx := t.Context()
	go b.watchdog(ctx)

	if !waitFor(500*time.Millisecond, func() bool {
		return logs.count("slack inbound quiet") >= 1
	}) {
		t.Fatal("watchdog never reported the quiet stream")
	}

	// Inbound comes back.
	b.lastInboundAt.Store(time.Now().Unix())
	if !waitFor(500*time.Millisecond, func() bool {
		return logs.count("slack inbound resumed") >= 1
	}) {
		t.Fatal("watchdog never reported the stream resuming")
	}

	// Going quiet again is a NEW transition and warns again.
	b.lastInboundAt.Store(time.Now().Add(-time.Hour).Unix())
	if !waitFor(500*time.Millisecond, func() bool {
		return logs.count("slack inbound quiet") >= 2
	}) {
		t.Fatal("a second quiet transition did not warn")
	}
}
