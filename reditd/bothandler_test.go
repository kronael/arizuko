package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

// redditMock records form-encoded requests keyed by path.
type redditMock struct {
	mu    sync.Mutex
	forms map[string]url.Values
	paths []string
}

func newRedditMock() *redditMock {
	return &redditMock{forms: map[string]url.Values{}}
}

func (m *redditMock) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/submit", m.recordForm)
	mux.HandleFunc("/api/comment", m.recordForm)
	mux.HandleFunc("/api/del", m.recordForm)
	return mux
}

func (m *redditMock) recordForm(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	m.mu.Lock()
	m.forms[r.URL.Path] = r.PostForm
	m.paths = append(m.paths, r.URL.Path)
	m.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"json":{"data":{"id":"abc123"}}}`))
}

func TestBotHandler_Send(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	rc := makeRedditClient(t, srv)
	rc.cfg.Username = "alice"

	// Without ReplyTo → /api/submit.
	_, err := rc.Send(chanlib.SendRequest{ChatJID: "reddit:alice", Content: "hello"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	form, ok := m.forms["/api/submit"]
	if !ok {
		t.Fatalf("no /api/submit recorded; paths=%v", m.paths)
	}
	if form.Get("text") != "hello" {
		t.Errorf("text = %q", form.Get("text"))
	}
	if form.Get("sr") != "u_alice" {
		t.Errorf("sr = %q", form.Get("sr"))
	}
}

func TestBotHandler_Post(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	rc := makeRedditClient(t, srv)
	rc.cfg.Username = "bob"

	if _, err := rc.Post(chanlib.PostRequest{ChatJID: "reddit:bob", Content: "post text"}); err != nil {
		t.Fatalf("Post: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	form, ok := m.forms["/api/submit"]
	if !ok {
		t.Fatalf("no /api/submit; paths=%v", m.paths)
	}
	if form.Get("text") != "post text" {
		t.Errorf("text = %q", form.Get("text"))
	}
}

func TestBotHandler_DeletePost(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	rc := makeRedditClient(t, srv)
	if err := rc.DeletePost(chanlib.DeleteRequest{
		ChatJID: "reddit:bob", TargetID: "t3_abc",
	}); err != nil {
		t.Fatalf("DeletePost: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	form, ok := m.forms["/api/del"]
	if !ok {
		t.Fatalf("no /api/del; paths=%v", m.paths)
	}
	if form.Get("id") != "t3_abc" {
		t.Errorf("id = %q", form.Get("id"))
	}
}
