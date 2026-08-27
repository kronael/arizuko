package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// /health reads isConnected. A value that never goes stale makes the adapter
// report healthy while its poll loop has been dead for hours, which is exactly
// the "healthchecks green but the agent doesn't reply" case the runbook warns
// about.
func TestIsConnectedGoesStaleAfterTheTolerance(t *testing.T) {
	rc := &redditClient{}
	if rc.isConnected() {
		t.Error("a client that has never polled reported connected")
	}

	rc.markPollOK()
	if !rc.isConnected() {
		t.Error("a client that just polled reported disconnected")
	}

	rc.lastPollOK.Store(time.Now().Add(-pollStaleAfter + time.Minute).Unix())
	if !rc.isConnected() {
		t.Errorf("a poll %s ago is inside the %s tolerance and must read connected",
			pollStaleAfter-time.Minute, pollStaleAfter)
	}

	rc.lastPollOK.Store(time.Now().Add(-pollStaleAfter - time.Minute).Unix())
	if rc.isConnected() {
		t.Errorf("a poll older than %s must flip /health to 503", pollStaleAfter)
	}
}

func TestLastInboundAtReportsWhatWasStored(t *testing.T) {
	rc := &redditClient{}
	if got := rc.LastInboundAt(); got != 0 {
		t.Errorf("a fresh client reported inbound at %d, want 0", got)
	}
	now := time.Now().Unix()
	rc.lastInboundAt.Store(now)
	if got := rc.LastInboundAt(); got != now {
		t.Errorf("LastInboundAt = %d, want %d", got, now)
	}
}

// The cursors file is what stops the adapter re-delivering a subreddit's whole
// recent history on every restart. It must survive a round trip, and a
// corrupted one must start fresh rather than crash the daemon at boot.
func TestCursorsRoundTripAndSurviveCorruption(t *testing.T) {
	dir := t.TempDir()
	rc := &redditClient{cfg: config{DataDir: dir}, cursors: map[string]string{}}

	rc.cursors["golang"] = "t3_abc"
	rc.cursors["rust"] = "t3_def"
	rc.saveCursors()

	back := &redditClient{cfg: config{DataDir: dir}, cursors: map[string]string{}}
	back.loadCursors()
	if back.cursors["golang"] != "t3_abc" || back.cursors["rust"] != "t3_def" {
		t.Errorf("cursors = %#v, want both restored", back.cursors)
	}

	// The file is per-account state; 0600 keeps it off a shared data dir.
	info, err := os.Stat(filepath.Join(dir, "cursors.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("cursors.json mode = %v, want 0600", info.Mode().Perm())
	}

	// Corrupted: start fresh, do not crash and do not keep half a map.
	if err := os.WriteFile(filepath.Join(dir, "cursors.json"), []byte(`{"golang":`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken := &redditClient{cfg: config{DataDir: dir}, cursors: map[string]string{}}
	broken.loadCursors()
	if len(broken.cursors) != 0 {
		t.Errorf("a corrupted cursors.json produced %#v, want an empty map", broken.cursors)
	}

	// Absent: also fresh, silently — a first boot has no file.
	missing := &redditClient{cfg: config{DataDir: t.TempDir()}, cursors: map[string]string{}}
	missing.loadCursors()
	if len(missing.cursors) != 0 {
		t.Errorf("a missing cursors.json produced %#v", missing.cursors)
	}
}

// ---------------------------------------------------------------------------
// doWithRetry
// ---------------------------------------------------------------------------

func TestDoWithRetryPassesANormalResponseStraightThrough(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	rc := &redditClient{http: srv.Client(), files: chanlib.NewURLCache(1)}
	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rc.doWithRetry(req)
	if err != nil {
		t.Fatalf("doWithRetry: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls != 1 {
		t.Errorf("status %d after %d calls, want 200 after 1", resp.StatusCode, calls)
	}
}

// A persistently rate-limited endpoint must end in an error rather than loop.
// The attempt count is deliberately NOT pinned here — see the skipped test
// below and BUGS L124.
func TestDoWithRetryGivesUpOnAPersistentRateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	rc := &redditClient{http: srv.Client()}
	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rc.doWithRetry(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a permanently rate-limited endpoint returned a response")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("the error must say the call was rate limited, got %v", err)
	}
}

// BUGS L124: doWithRetry's own 429 loop sits on top of chanlib.DoWithRetry,
// which already retries 429 three times and honours Retry-After itself. One
// logical call therefore makes NINE HTTP requests against a rate limiter, and
// the outer clamp (5 minutes) contradicts chanlib's documented 30s cap.
// Measured in this tree: 9 requests, 8.76s, against a Retry-After of 1.
//
// Skipped rather than deleted: the entry is open and this is the assertion the
// fix has to satisfy. Un-skip with the fix.
func TestDoWithRetryDoesNotStackTwoRetryLayers(t *testing.T) {
	t.Skip("BUGS L124 — open: reditd's 429 loop wraps chanlib's, so one call makes 9 requests")

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	rc := &redditClient{http: srv.Client()}
	req, err := http.NewRequest("GET", srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp, err := rc.doWithRetry(req); err == nil {
		resp.Body.Close()
	}
	if n := calls.Load(); n > 3 {
		t.Errorf("%d HTTP requests for one logical call; two retry layers are stacked", n)
	}
}

func TestTypingIsASilentNoOp(t *testing.T) {
	(&redditClient{}).Typing("reddit:t3_abc", true)
	(&redditClient{}).Typing("reddit:t3_abc", false)
}
