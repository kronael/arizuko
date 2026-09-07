// Package testutils provides shared fixtures for daemon integration tests:
// a migrated DB (store or routd schema), a FakePlatform httptest server, and
// small assertion helpers.
package testutils

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// Inst is a minimal per-test fixture. Tmp is a temp dir. DB is an open,
// migrated in-memory sqlite handle. Store wraps DB. ChanReg is an empty
// registry whose HMAC secret matches JWTSecret.
type Inst struct {
	DB        *sql.DB
	Store     *store.Store
	Tmp       string
	JWTSecret []byte
	ChanReg   *chanreg.Registry
}

// NewRoutdInstance builds an Inst whose DB carries the ROUTD schema — the one
// routd.db actually has in production. Daemons that read routd.db (dashd) must
// use this, not NewInstance: the two schemas overlap on messages/groups/acl/
// routes but diverge on cost_log (recorded_at/cost_cents/input_tokens vs the
// store's ts/cents/input_tok) and on whatever routd's own migration chain adds
// next, so a store-schema fixture lets a genuine column-name regression pass.
// NewInstance stays for callers whose subject IS the frozen pre-split schema.
// Registers t.Cleanup to close the DB.
func NewRoutdInstance(t *testing.T) *Inst {
	t.Helper()
	tmp := t.TempDir()
	rdb, err := routd.Create(filepath.Join(tmp, "store"))
	if err != nil {
		t.Fatalf("routd.Open: %v", err)
	}
	db := rdb.SQL()
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	inst := &Inst{
		DB:        db,
		Store:     store.New(db),
		Tmp:       tmp,
		JWTSecret: secret,
		ChanReg:   chanreg.New(),
	}
	t.Cleanup(func() { rdb.Close() })
	return inst
}

// NewRoutdStore is NewRoutdInstance for the many tests that want only the
// *store.Store: routd.db's own schema, wrapped in the shared read/write library
// six daemons hold in production. It is what a test reaches for instead of
// store.OpenMem, whose chain is the frozen pre-split messages.db (see
// store.Migrate). Registers t.Cleanup to release the database.
func NewRoutdStore(t *testing.T) *store.Store {
	t.Helper()
	d, err := routd.OpenMem()
	if err != nil {
		t.Fatalf("routd.OpenMem: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return store.New(d.SQL())
}

// NewInstance builds an Inst on the FROZEN pre-split messages.db schema. It is
// for a caller whose subject IS that database — `arizuko migrate-split` and the
// fixtures that simulate an old instance for it. Every other test wants
// NewRoutdInstance or NewRoutdStore: routd.db is the schema production has, and
// the two chains have already drifted (routd seeds role:member and role:operator
// into acl, store's chain seeds neither). Registers t.Cleanup to close the DB.
func NewInstance(t *testing.T) *Inst {
	t.Helper()
	tmp := t.TempDir()
	db, err := db_utils.Open(db_utils.FileDSN(filepath.Join(tmp, "test.db")))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Migrate(db); err != nil {
		db.Close()
		t.Fatalf("migrate: %v", err)
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		t.Fatalf("rand: %v", err)
	}
	inst := &Inst{
		DB:        db,
		Store:     store.New(db),
		Tmp:       tmp,
		JWTSecret: secret,
		ChanReg:   chanreg.New(),
	}
	t.Cleanup(func() { db.Close() })
	return inst
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
		json.NewDecoder(req.Body).Decode(&im)
		r.Mu.Lock()
		r.Msgs = append(r.Msgs, im)
		r.Mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
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
func AssertMessage(t *testing.T, db *sql.DB, jid, substr string) {
	t.Helper()
	rows, err := db.Query(`SELECT content FROM messages WHERE chat_jid = ?`, jid)
	if err != nil {
		t.Fatalf("query messages: %v", err)
	}
	defer rows.Close()
	var found []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if strings.Contains(c, substr) {
			return
		}
		found = append(found, c)
	}
	t.Fatalf("no message for jid=%q containing %q; saw %v", jid, substr, found)
}

// WaitForRow polls SELECT COUNT(*) from the given query until > 0 or timeout.
// query must be a full SELECT returning an int count.
func WaitForRow(t *testing.T, db *sql.DB, query string, args []any, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err == nil && n > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("WaitForRow timeout after %s: %s args=%v", timeout, query, args)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
