package chanlib

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type registrationTransport struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (t *registrationTransport) RoundTrip(*http.Request) (*http.Response, error) {
	if t.calls.Add(1) == 2 {
		close(t.entered)
		<-t.release
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true,"token":"registered"}`)),
		Header:     make(http.Header),
	}, nil
}

// Refresh and capability updates preserve the new capabilities in either order.
func TestRegistrationKeepsNewCaps(t *testing.T) {
	for _, setFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "refresh first", true: "set caps first"}[setFirst], func(t *testing.T) {
			transport := &registrationTransport{entered: make(chan struct{}), release: make(chan struct{})}
			rc := NewRouterClient("http://router.test")
			rc.client.Transport = transport
			if _, err := rc.Register("test", "http://adapter", nil, map[string]bool{"send_text": false}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 2)
			secondDone := make(chan struct{})
			for i := range 2 {
				go func() {
					if (i == 0) == setFirst {
						done <- rc.SetCaps(map[string]bool{"send_text": true})
					} else {
						done <- rc.reregister()
					}
					if i == 1 {
						close(secondDone)
					}
				}()
				if i == 0 {
					<-transport.entered
				}
			}
			select {
			case <-secondDone:
			case <-time.After(50 * time.Millisecond):
			}
			close(transport.release)
			for range 2 {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
			_, _, _, caps := rc.savedRegistration()
			if !caps["send_text"] {
				t.Fatal("registration restored stale capabilities")
			}
		})
	}
}

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
