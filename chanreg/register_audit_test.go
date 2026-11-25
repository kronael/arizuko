package chanreg

import (
	"context"
	"sync"
	"testing"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// auditSink records the events audit.Emit writes.
type auditSink struct {
	mu      sync.Mutex
	actions []string
}

func (s *auditSink) Query(context.Context, objstore.Query) ([]objstore.Item, error) {
	return nil, nil
}

func (s *auditSink) GuardedBatch(_ context.Context, _ []objstore.Check,
	writes []objstore.Write) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, w := range writes {
		s.actions = append(s.actions, w.Attrs["action"])
	}
	return true, nil
}

func (s *auditSink) count(action string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.actions {
		if a == action {
			n++
		}
	}
	return n
}

// An adapter registers again every three minutes as its heartbeat. On sloth
// that heartbeat was 359,291 of the 414,506 rows of routd's audit_log. A
// registration is recorded when it changes something, and a heartbeat changes
// nothing.
func TestAnUnchangedRegistrationIsNotAudited(t *testing.T) {
	sink := &auditSink{}
	audit.Init(sink, "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	r := New()
	register := func(url string, prefixes []string, caps map[string]bool, ip string) string {
		t.Helper()
		token, err := r.RegisterWithOrigin("teled", url, prefixes, caps, ip, "service:teled")
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	url, prefixes := "http://10.0.0.11:8080", []string{"telegram:"}
	caps := map[string]bool{"send_text": true}

	first := register(url, prefixes, caps, "172.18.0.4")
	for range 3 {
		register(url, []string{"telegram:"}, map[string]bool{"send_text": true}, "172.18.0.4")
	}
	if n := sink.count("channel.register"); n != 1 {
		t.Fatalf("one registration and three heartbeats wrote %d audit rows, want 1", n)
	}
	if last := r.Get("teled").Token; last == first || r.ByToken(first) != nil {
		t.Error("a heartbeat must still rotate the session token")
	}

	for i, change := range []func(){
		func() { register("http://10.0.0.12:8080", prefixes, caps, "172.18.0.4") },
		func() { register("http://10.0.0.12:8080", []string{"telegram:", "tg:"}, caps, "172.18.0.4") },
		func() {
			register("http://10.0.0.12:8080", []string{"telegram:", "tg:"},
				map[string]bool{"send_text": true, "typing": true}, "172.18.0.4")
		},
		func() {
			register("http://10.0.0.12:8080", []string{"telegram:", "tg:"},
				map[string]bool{"send_text": true, "typing": true}, "172.18.0.9")
		},
	} {
		change()
		if n := sink.count("channel.register"); n != i+2 {
			t.Fatalf("change %d left %d audit rows, want %d", i, n, i+2)
		}
	}

	r.Deregister("teled")
	register("http://10.0.0.12:8080", []string{"telegram:", "tg:"},
		map[string]bool{"send_text": true, "typing": true}, "172.18.0.9")
	if n := sink.count("channel.register"); n != 6 {
		t.Errorf("a registration after a deregistration left %d audit rows, want 6", n)
	}
}
