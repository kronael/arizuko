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

// signChatHeaders supplies the proxyd-stamped route-token headers webd's
// chatTransit gate trusts (nil KeySet → trusted directly).
func signChatHeaders(token, folder string) map[string]string {
	return map[string]string{
		"X-Chat-Token": token,
		"X-Folder":     folder,
	}
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

func newMockRouter() *mockRouter              { return newMockRouterWithStore(nil) }
func newMockRouterWithStore(st *store.Store) *mockRouter {
	m := &mockRouter{st: st}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var msg chanlib.InboundMsg
		_ = json.NewDecoder(r.Body).Decode(&msg)
		m.mu.Lock()
		m.messages = append(m.messages, msg)
		m.mu.Unlock()
		// Persist to store (simulates routd), enabling read-back tests.
		// Use time.Now() with full nanosecond precision — time.Unix(seconds, 0)
		// produces "...T10:15:47Z" which SQLite string-compares as > "...T10:15:47.nnnZ"
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
// In tests, st serves as both messages.db and routd.db (single in-memory DB).
// The mock router persists inbound messages to st (simulates routd).
func newTestServer(t *testing.T) (*server, *mockRouter, *store.Store) {
	t.Helper()
	st, err := store.OpenMem()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mr := newMockRouterWithStore(st)
	t.Cleanup(mr.close)

	rc := chanlib.NewRouterClient(mr.srv.URL)
	rc.SetToken("test-token")

	cfg := config{assistantName: "assistant"}
	// st serves as both messages.db (legacy) and routd.db (live reads)
	return newServer(cfg, st, st, newHub(), rc, nil, nil), mr, st
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

func TestUserAllowedFolder(t *testing.T) {
	cases := []struct {
		grants []string
		folder string
		want   bool
	}{
		{[]string{"**"}, "anything", true},
		{[]string{"atlas"}, "atlas", true},
		{[]string{"atlas"}, "atlas/content", true},
		{[]string{"atlas"}, "atlaswhatever", false},
		{[]string{"pub"}, "other", false},
		{nil, "main", false},
	}
	for _, c := range cases {
		if got := userAllowedFolder(c.grants, c.folder); got != c.want {
			t.Errorf("userAllowedFolder(%v, %q) = %v, want %v",
				c.grants, c.folder, got, c.want)
		}
	}
}
