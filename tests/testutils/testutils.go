// Package testutils provides shared fixtures for daemon integration tests: a
// prepared store, a FakePlatform httptest server, and small assertion helpers.
package testutils

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// Inst is a minimal per-test fixture. Tmp is a temp dir. Store is routd's own
// tables in a namespace this test owns. ChanReg is an empty registry whose HMAC
// secret matches JWTSecret.
type Inst struct {
	Store     *store.Store
	Tmp       string
	JWTSecret []byte
	ChanReg   *chanreg.Registry
}

// NewRoutdInstance builds an Inst over routd's OWN tables — the ones production
// has. Registers t.Cleanup to drop the namespace.
func NewRoutdInstance(t *testing.T) *Inst {
	t.Helper()
	rdb, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return &Inst{
		Store:     store.NewOver(rdb.Obj()),
		Tmp:       t.TempDir(),
		JWTSecret: secret,
		ChanReg:   chanreg.New(),
	}
}

// BreakStore takes one entity away from s, which is how a test makes a read
// fail on purpose. The store is a network service, so "cannot answer" is an
// ordinary condition — an endpoint that is down, a key whose policy no longer
// covers the table, an owner that has never booted — and a missing entity is the
// one a test can make deterministic.
func BreakStore(t *testing.T, s *store.Store, entities ...string) {
	t.Helper()
	d, ok := s.Obj().(*dynstore.Store)
	if !ok {
		t.Fatalf("this fixture is not served: %T", s.Obj())
	}
	for _, e := range entities {
		if err := d.DropEntity(context.Background(), e); err != nil {
			t.Fatalf("drop %s: %v", e, err)
		}
	}
}

// NewOwnerStore is the store with BOTH owners' entities made — routd's and
// onbod's. It is for a test whose subject crosses them: the invite lifecycle
// writes onbod's `invites` and reads routd's `acl`, and no daemon holds both,
// so no daemon's door builds this. Registers t.Cleanup to drop the namespace.
func NewOwnerStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatalf("store.OpenMem: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// NewRoutdStore is NewRoutdInstance for the many tests that want only the
// *store.Store: routd's own tables, wrapped in the shared read/write library six
// daemons hold in production. Registers t.Cleanup to drop the namespace.
func NewRoutdStore(t *testing.T) *store.Store {
	t.Helper()
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return store.NewOver(d.Obj())
}

// PlatformReq is a recorded inbound request to a FakePlatform.
type PlatformReq struct {
	Method string
	Path   string
	Body   []byte
	Header http.Header
}

// RouterStub is the routd an adapter test delivers to: POST /v1/messages
// records each InboundMsg in Msgs, or answers 500 while Fail is set. Mux is
// open for the routes only one adapter needs (slakd's pane writes).
type RouterStub struct {
	Mux  *http.ServeMux
	Srv  *httptest.Server
	Fail bool
	Mu   sync.Mutex
	Msgs []chanlib.InboundMsg
}

func NewRouterStub() *RouterStub {
	r := &RouterStub{Mux: http.NewServeMux()}
	r.Mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, req *http.Request) {
		if r.Fail {
			w.WriteHeader(500)
			return
		}
		var im chanlib.InboundMsg
		if err := json.NewDecoder(req.Body).Decode(&im); err != nil {
			// routd's own handler 400s a body it cannot read; a stub that answers ok
			// and records a zero message hides the producer that sent it.
			w.WriteHeader(400)
			return
		}
		r.Mu.Lock()
		r.Msgs = append(r.Msgs, im)
		r.Mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	r.Srv = httptest.NewServer(r.Mux)
	return r
}

// Snapshot copies Msgs under the lock, for a reader racing the adapter's
// delivery goroutine.
func (r *RouterStub) Snapshot() []chanlib.InboundMsg {
	r.Mu.Lock()
	defer r.Mu.Unlock()
	out := make([]chanlib.InboundMsg, len(r.Msgs))
	copy(out, r.Msgs)
	return out
}

func (r *RouterStub) Close() { r.Srv.Close() }

// PlatformHandler produces the response body (and optional status) for a path.
type PlatformHandler func(req PlatformReq) (status int, body any)

// FakePlatform is a configurable httptest.Server that records requests and
// returns canned JSON per "METHOD /path" route.
type FakePlatform struct {
	srv      *httptest.Server
	mu       sync.Mutex
	handlers map[string]PlatformHandler
	reqs     []PlatformReq
}

// NewFakePlatform starts a server. Close with Close().
func NewFakePlatform() *FakePlatform {
	p := &FakePlatform{handlers: map[string]PlatformHandler{}}
	p.srv = httptest.NewServer(http.HandlerFunc(p.handle))
	return p
}

// On registers a handler for "METHOD /path".
func (p *FakePlatform) On(route string, h PlatformHandler) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.handlers[route] = h
}

// URL returns the server's base URL.
func (p *FakePlatform) URL() string { return p.srv.URL }

// Close shuts down the server.
func (p *FakePlatform) Close() { p.srv.Close() }

// Requests returns a snapshot of all recorded requests.
func (p *FakePlatform) Requests() []PlatformReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]PlatformReq, len(p.reqs))
	copy(cp, p.reqs)
	return cp
}

func (p *FakePlatform) handle(w http.ResponseWriter, r *http.Request) {
	body := make([]byte, 0, 512)
	if r.Body != nil {
		buf := make([]byte, 1<<20)
		for {
			n, err := r.Body.Read(buf)
			if n > 0 {
				body = append(body, buf[:n]...)
			}
			if err != nil {
				break
			}
		}
	}
	req := PlatformReq{Method: r.Method, Path: r.URL.Path, Body: body, Header: r.Header.Clone()}
	p.mu.Lock()
	p.reqs = append(p.reqs, req)
	h := p.handlers[r.Method+" "+r.URL.Path]
	p.mu.Unlock()

	if h == nil {
		http.NotFound(w, r)
		return
	}
	status, resp := h(req)
	if status == 0 {
		status = http.StatusOK
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if resp != nil {
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// AssertMessage fails if no row in messages has chat_jid=jid and content
// containing substr.
func AssertMessage(t *testing.T, s *store.Store, jid, substr string) {
	t.Helper()
	msgs, err := s.MessagesSince(jid, time.Time{})
	if err != nil {
		t.Fatalf("read %s: %v", jid, err)
	}
	var found []string
	for _, m := range msgs {
		if strings.Contains(m.Content, substr) {
			return
		}
		found = append(found, m.Content)
	}
	t.Fatalf("no message for jid=%q containing %q; saw %v", jid, substr, found)
}

// WaitFor polls until holds() answers true, or fails naming what it waited for.
// It is the one poll loop here, and every caller passes the read itself.
func WaitFor(t *testing.T, what string, timeout time.Duration, holds func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if holds() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout after %s waiting for %s", timeout, what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// BotRows counts one chat's bot rows — the agent's own output. `messages` is an
// entity, so the count is a partition read rather than a column query, and one
// reader answers it for every fixture that used to write its own SELECT.
func BotRows(t *testing.T, s *store.Store, chatJID string) int {
	t.Helper()
	msgs, err := s.MessagesSince(chatJID, time.Time{})
	if err != nil {
		t.Fatalf("read %s: %v", chatJID, err)
	}
	n := 0
	for _, m := range msgs {
		if m.BotMsg {
			n++
		}
	}
	return n
}

// WaitForBotRow waits until a chat carries the agent's reply.
func WaitForBotRow(t *testing.T, s *store.Store, chatJID string, timeout time.Duration) {
	t.Helper()
	WaitFor(t, "a bot row in "+chatJID, timeout, func() bool {
		msgs, err := s.MessagesSince(chatJID, time.Time{})
		if err != nil {
			return false
		}
		for _, m := range msgs {
			if m.BotMsg {
				return true
			}
		}
		return false
	})
}
