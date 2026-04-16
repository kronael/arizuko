package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

// mockRouter is a minimal fake of the router API used by webd.
type mockRouter struct {
	mu       sync.Mutex
	messages []chanlib.InboundMsg
	srv      *httptest.Server
}

func newMockRouter() *mockRouter {
	m := &mockRouter{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var msg chanlib.InboundMsg
		_ = json.NewDecoder(r.Body).Decode(&msg)
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
func newTestServer(t *testing.T) (*server, *mockRouter, *store.Store) {
	t.Helper()
	st, err := store.OpenMem()
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	mr := newMockRouter()
	t.Cleanup(mr.close)

	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("test-token")

	cfg := config{assistantName: "assistant"}
	return newServer(cfg, st, newHub(), rc), mr, st
}

func seedGroup(t *testing.T, st *store.Store, folder, name string) core.Group {
	t.Helper()
	g := core.Group{
		Name: name, Folder: folder, AddedAt: time.Now(),
		SlinkToken: "tok-" + folder,
	}
	if err := st.PutGroup(g); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	got, _ := st.GroupByFolder(folder)
	return got
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
