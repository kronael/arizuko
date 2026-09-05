package chanlib

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// registerCounter is an httptest server that accepts registrations and counts
// them, so a test can watch the heartbeat replay one.
func registerCounter(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/channels/register" {
			n.Add(1)
		}
		w.Write([]byte(`{"ok":true,"token":"tok"}`))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// TestHeartbeatReRegisters: routd auto-deregisters a channel after 3 failed
// health probes, and a deregistered adapter keeps taking inbound while its
// outbound silently fails. Every registering daemon runs this, so the replay
// itself must work off the saved registration with no caller state.
func TestHeartbeatReRegisters(t *testing.T) {
	srv, calls := registerCounter(t)

	rc := NewRouterClient(srv.URL)
	if _, err := rc.Register("web", "http://webd:8080", []string{"web:"}, map[string]bool{"send_text": true}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("register calls = %d, want 1", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rc.heartbeat(ctx, 2*time.Millisecond)

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 3 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := calls.Load(); got < 3 {
		t.Fatalf("heartbeat replayed %d registrations, want at least 3", got)
	}

	// The replay carries the ORIGINAL parameters, not zero values.
	name, url, prefixes, caps := rc.savedRegistration()
	if name != "web" || url != "http://webd:8080" || len(prefixes) != 1 || !caps["send_text"] {
		t.Fatalf("saved registration drifted: %q %q %v %v", name, url, prefixes, caps)
	}
}

// TestHeartbeatStopsOnContext: the heartbeat is a daemon-lifetime goroutine and
// must end with the daemon, not outlive it.
func TestHeartbeatStopsOnContext(t *testing.T) {
	srv, calls := registerCounter(t)

	rc := NewRouterClient(srv.URL)
	if _, err := rc.Register("web", "http://webd:8080", []string{"web:"}, nil); err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		rc.heartbeat(ctx, 2*time.Millisecond)
	}()

	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not stop on context cancel")
	}

	after := calls.Load()
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != after {
		t.Fatalf("heartbeat kept registering after cancel: %d -> %d", after, got)
	}
}

// TestHeartbeatWithoutRegisterIsQuiet: a client that never registered has
// nothing to replay and must not POST a nameless registration.
func TestHeartbeatWithoutRegisterIsQuiet(t *testing.T) {
	srv, calls := registerCounter(t)

	rc := NewRouterClient(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go rc.heartbeat(ctx, time.Millisecond)

	time.Sleep(30 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("heartbeat registered %d times with no saved registration, want 0", got)
	}
}
