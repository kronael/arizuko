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
	mux.HandleFunc("/api/vote", m.recordForm)
	mux.HandleFunc("/api/editusertext", m.recordForm)
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

func TestBotHandler_Delete(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	rc := makeRedditClient(t, srv)
	if err := rc.Delete(chanlib.DeleteRequest{
		ChatJID: "reddit:bob", TargetID: "t3_abc",
	}); err != nil {
		t.Fatalf("Delete: %v", err)
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

func TestBotHandler_UnsupportedHints_Reditd(t *testing.T) {
	rc := &redditClient{}
	if _, err := rc.Forward(chanlib.ForwardRequest{}); !reditdHasHint(err) {
		t.Errorf("forward: missing hint err=%v", err)
	}
	if _, err := rc.Quote(chanlib.QuoteRequest{}); !reditdHasHint(err) {
		t.Errorf("quote: missing hint err=%v", err)
	}
	if _, err := rc.Repost(chanlib.RepostRequest{}); !reditdHasHint(err) {
		t.Errorf("repost: missing hint err=%v", err)
	}
}

func TestBotHandler_Edit_Reditd(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	rc := makeRedditClient(t, srv)

	if err := rc.Edit(chanlib.EditRequest{TargetID: "t1_abc", Content: "edited"}); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	form, ok := m.forms["/api/editusertext"]
	if !ok {
		t.Fatalf("no /api/editusertext; paths=%v", m.paths)
	}
	if form.Get("thing_id") != "t1_abc" {
		t.Errorf("thing_id = %q", form.Get("thing_id"))
	}
	if form.Get("text") != "edited" {
		t.Errorf("text = %q", form.Get("text"))
	}
}

func TestBotHandler_LikeDislike_Reditd(t *testing.T) {
	m := newRedditMock()
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	rc := makeRedditClient(t, srv)

	if err := rc.Like(chanlib.LikeRequest{TargetID: "t3_abc"}); err != nil {
		t.Fatalf("Like: %v", err)
	}
	if err := rc.Dislike(chanlib.DislikeRequest{TargetID: "t1_def"}); err != nil {
		t.Fatalf("Dislike: %v", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if got := m.forms["/api/vote"].Get("dir"); got != "-1" {
		// last write wins; map keeps only the last form per path
		t.Errorf("/api/vote dir = %q, want -1 (after Dislike)", got)
	}
	if got := m.forms["/api/vote"].Get("id"); got != "t1_def" {
		t.Errorf("/api/vote id = %q, want t1_def", got)
	}
	// at least two votes recorded
	var voteCount int
	for _, p := range m.paths {
		if p == "/api/vote" {
			voteCount++
		}
	}
	if voteCount != 2 {
		t.Errorf("vote count = %d, want 2", voteCount)
	}
}

func reditdHasHint(err error) bool {
	if err == nil {
		return false
	}
	ue, ok := err.(*chanlib.UnsupportedError)
	return ok && ue.Hint != ""
}
