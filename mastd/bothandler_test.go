package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mattn/go-mastodon"

	"github.com/onvos/arizuko/chanlib"
)

// newTestMasto returns a mastoClient pointing at the given base URL.
// No auth — the stub server accepts any Authorization header.
func newTestMasto(t *testing.T, baseURL string) *mastoClient {
	t.Helper()
	c := mastodon.NewClient(&mastodon.Config{Server: baseURL, AccessToken: "tok"})
	return &mastoClient{
		cfg:    config{Name: "mastodon", InstanceURL: baseURL, ListenURL: baseURL},
		client: c,
		http:   &http.Client{},
		files:  chanlib.NewURLCache(10),
	}
}

type mastoMock struct {
	srv  *httptest.Server
	reqs []*http.Request
	// bodies keeps a copy of captured bodies by index (matches reqs).
	bodies []string
}

func newMastoMock(t *testing.T) *mastoMock {
	m := &mastoMock{}
	mux := http.NewServeMux()
	handle := func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		m.reqs = append(m.reqs, r)
		m.bodies = append(m.bodies, string(buf[:n]))
		w.Header().Set("Content-Type", "application/json")
		// A status response suitable for PostStatus / Favourite.
		if r.Method == http.MethodDelete {
			w.Write([]byte(`{}`))
			return
		}
		w.Write([]byte(`{"id":"status-1","content":"ok"}`))
	}
	mux.HandleFunc("/api/v1/statuses", handle)
	mux.HandleFunc("/api/v1/statuses/", handle)
	m.srv = httptest.NewServer(mux)
	t.Cleanup(m.srv.Close)
	return m
}

func (m *mastoMock) paths() []string {
	out := make([]string, len(m.reqs))
	for i, r := range m.reqs {
		out[i] = r.Method + " " + r.URL.Path
	}
	return out
}

func TestBotHandler_Send(t *testing.T) {
	m := newMastoMock(t)
	mc := newTestMasto(t, m.srv.URL)

	_, err := mc.Send(chanlib.SendRequest{ChatJID: "mastodon:user", Content: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !contains(m.paths(), "POST /api/v1/statuses") {
		t.Errorf("no POST /api/v1/statuses; got %v", m.paths())
	}
}

func TestBotHandler_Post(t *testing.T) {
	m := newMastoMock(t)
	mc := newTestMasto(t, m.srv.URL)

	id, err := mc.Post(chanlib.PostRequest{ChatJID: "mastodon:user", Content: "post body"})
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if id != "status-1" {
		t.Errorf("id = %q", id)
	}
	if !contains(m.paths(), "POST /api/v1/statuses") {
		t.Errorf("missing POST /api/v1/statuses: %v", m.paths())
	}
}

func TestBotHandler_React(t *testing.T) {
	m := newMastoMock(t)
	mc := newTestMasto(t, m.srv.URL)

	if err := mc.React(chanlib.ReactRequest{
		ChatJID: "mastodon:user", TargetID: "42", Reaction: "⭐",
	}); err != nil {
		t.Fatalf("React: %v", err)
	}
	ok := false
	for _, p := range m.paths() {
		if strings.HasPrefix(p, "POST /api/v1/statuses/42/favourite") {
			ok = true
		}
	}
	if !ok {
		t.Errorf("no favourite call: %v", m.paths())
	}
}

func TestBotHandler_DeletePost(t *testing.T) {
	m := newMastoMock(t)
	mc := newTestMasto(t, m.srv.URL)

	if err := mc.DeletePost(chanlib.DeleteRequest{
		ChatJID: "mastodon:user", TargetID: "99",
	}); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}
	if !contains(m.paths(), "DELETE /api/v1/statuses/99") {
		t.Errorf("no DELETE: %v", m.paths())
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
