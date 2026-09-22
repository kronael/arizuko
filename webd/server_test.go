package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// signUserHeaders supplies the proxyd-stamped identity headers webd's
// requireUser gate trusts. The test servers run with a nil KeySet (no AUTHD_URL,
// local-dev path), so identified() trusts the stamped X-User-Sub directly — no
// transit bearer needed. Named for the pre-ES256 era; it now just sets headers.
func signUserHeaders(h map[string]string) map[string]string {
	if h == nil {
		h = map[string]string{}
	}
	return h
}

// mockRouter is a minimal fake of the router API used by webd. When st is
// non-nil, inbound messages are persisted to simulate routd's behavior; tests
// can then read them back from the store (matching production where routd
// persists and webd reads from routd.db).
type mockRouter struct {
	mu       sync.Mutex
	messages []chanlib.InboundMsg
	srv      *httptest.Server
	st       *store.Store
}

func newMockRouterWithStore(st *store.Store) *mockRouter {
	m := &mockRouter{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var msg chanlib.InboundMsg
		_ = json.NewDecoder(r.Body).Decode(&msg)
		// Persist BEFORE publishing the arrival: sent() is the signal a test
		// waits on to play routd's reply, and webd's /send resolves the reply's
		// hub topic by reading this row back (TopicByMessageID). Announcing
		// first let the reply publish on an empty topic while the waiter sat on
		// the real one, so ?wait= timed out roughly one run in ten. Real routd
		// appends before it calls back; this keeps the same order.
		//
		// Use time.Now() with full nanosecond precision — time.Unix(seconds, 0)
		// produces "...T10:15:47Z", which string-compares as > "...T10:15:47.nnnZ"
		// because 'Z' (90) > '.' (46), breaking timestamp < ? queries.
		if m.st != nil {
			_ = m.st.PutMessage(core.Message{
				ID:        msg.ID,
				ChatJID:   msg.ChatJID,
				Sender:    msg.Sender,
				Name:      msg.SenderName,
				Content:   msg.Content,
				Timestamp: time.Now(),
				Topic:     msg.Topic,
				TurnID:    msg.ID, // user message is its own turn
				Source:    "web",
			})
		}
		m.mu.Lock()
		m.messages = append(m.messages, msg)
		m.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *mockRouter) close() { m.srv.Close() }

func (m *mockRouter) sent() []chanlib.InboundMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chanlib.InboundMsg, len(m.messages))
	copy(out, m.messages)
	return out
}

// newTestServer builds a server wired to in-memory store + mock router.
// Callers append groups via st.PutGroup before driving requests.
// In tests, st stands in for routd.db (single in-memory DB).
// The mock router persists inbound messages to st (simulates routd).
func newTestServer(t *testing.T) (*server, *mockRouter, *store.Store) {
	t.Helper()
	st := testutils.NewRoutdStore(t)

	mr := newMockRouterWithStore(st)
	t.Cleanup(mr.close)

	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("test-token")

	// routerURL mirrors production, where compose always pins ROUTER_URL. Left
	// empty, every fixture server looks like one with routd unreachable, which
	// makes "works without routd" untestable — the whole suite fails alike.
	cfg := config{assistantName: "assistant", routerURL: mr.srv.URL}
	return newServer(cfg, st, newHub(), rc, nil, nil), mr, st
}

func seedGroup(t *testing.T, st *store.Store, folder, name string) core.Group {
	t.Helper()
	_ = name
	g := core.Group{Folder: folder, AddedAt: time.Now()}
	if err := st.PutGroup(g); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	got, _ := st.GroupByFolder(folder)
	return got
}

// seedChatToken inserts a web: route token for folder and returns the raw token.
func seedChatToken(t *testing.T, st *store.Store, folder string) string {
	t.Helper()
	raw := store.GenRouteToken()
	rt := store.RouteToken{JID: "web:" + folder, OwnerFolder: folder}
	if err := st.InsertRouteToken(raw, rt); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	return raw
}

// TestRequireFolderMatchesGrantGlobs — webd's folder gate answers exactly what
// auth.MatchGroups answers, so a grant reaches the same folders on every
// surface. The scope pattern IS the containment (5/33-paths-roles decision 8): `atlas`
// covers `atlas` alone, `atlas/**` covers the subtree (BUGS L101).
func TestRequireFolderMatchesGrantGlobs(t *testing.T) {
	cases := []struct {
		grants []string
		folder string
		want   int
	}{
		{[]string{"**"}, "anything", http.StatusNoContent},
		{[]string{"atlas"}, "atlas", http.StatusNoContent},
		{[]string{"atlas"}, "atlas/content", http.StatusForbidden},
		{[]string{"atlas/**"}, "atlas", http.StatusNoContent},
		{[]string{"atlas/**"}, "atlas/content", http.StatusNoContent},
		{[]string{"atlas"}, "atlaswhatever", http.StatusForbidden},
		{[]string{"pub"}, "other", http.StatusForbidden},
		{nil, "main", http.StatusForbidden},
	}
	s := &server{}
	s.requireUser = func(next http.HandlerFunc) http.HandlerFunc { return next }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /panel/{folder...}", s.requireFolder(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/panel/"+c.folder, nil)
		g, _ := json.Marshal(c.grants)
		req.Header.Set("X-User-Groups", string(g))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("grants %v folder %q: status = %d, want %d",
				c.grants, c.folder, rec.Code, c.want)
		}
	}
}
