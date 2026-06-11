package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// newThreadDiscordServer mimics the two REST calls the reply-into-thread path
// makes: POST .../messages/<root>/threads (start thread → channel th-9) and
// POST /channels/<ch>/messages (send). threadStatus/threadBody override the
// thread-start response for the already-has-thread case.
func newThreadDiscordServer(t *testing.T, rec *recorded, threadStatus int, threadBody string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 4096)
		n, _ := r.Body.Read(body)
		rec.record(r.Method, r.URL.Path, string(body[:n]))
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/threads") {
			w.WriteHeader(threadStatus)
			w.Write([]byte(threadBody))
			return
		}
		w.Write([]byte(`{"id":"msg-ok","channel_id":"x"}`))
	}))
}

// A reply carrying ThreadRoot starts a thread from the trigger message and
// posts INTO the new thread channel; the reply-reference to the root (which
// lives in the parent channel) is dropped.
func TestSend_ThreadRootCreatesThread(t *testing.T) {
	rec := &recorded{}
	srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"th-9"}`)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	id, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "answer\nmore",
		ReplyTo: "root-1", ThreadRoot: "root-1",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "msg-ok" {
		t.Errorf("id = %q", id)
	}
	assertPath(t, rec, "POST", "/channels/ch-1/messages/root-1/threads")
	assertPath(t, rec, "POST", "/channels/th-9/messages")
	for _, r := range rec.snapshot() {
		if strings.HasSuffix(r.Path, "/threads") {
			var ts struct {
				Name string `json:"name"`
			}
			json.Unmarshal([]byte(r.Body), &ts)
			if ts.Name != "answer" {
				t.Errorf("thread name = %q, want first line of reply", ts.Name)
			}
		}
		if r.Path == "/channels/th-9/messages" && strings.Contains(r.Body, "message_reference") {
			t.Errorf("reply-reference to parent-channel root must be dropped: %s", r.Body)
		}
	}
}

// A root that already has a thread reuses it: Discord assigns a
// message-rooted thread the SAME id as its root message (code 160004).
func TestSend_ThreadRootAlreadyExists(t *testing.T) {
	rec := &recorded{}
	srv := newThreadDiscordServer(t, rec, http.StatusBadRequest,
		`{"code":160004,"message":"A message can only have one thread"}`)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "again", ThreadRoot: "root-1",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	assertPath(t, rec, "POST", "/channels/root-1/messages")
}

// send (no ThreadRoot) stays in the channel; an existing thread (ThreadID)
// wins over ThreadRoot; DMs never thread.
func TestSend_ChannelAndDMStayInline(t *testing.T) {
	cases := []struct {
		name string
		req  chanlib.SendRequest
		path string
	}{
		{"send stays channel",
			chanlib.SendRequest{ChatJID: "discord:g1/ch-1", Content: "announce"},
			"/channels/ch-1/messages"},
		{"existing thread wins",
			chanlib.SendRequest{ChatJID: "discord:g1/ch-1", Content: "re", ThreadID: "th-7", ThreadRoot: "root-1"},
			"/channels/th-7/messages"},
		{"dm replies inline",
			chanlib.SendRequest{ChatJID: "discord:dm/dm-1", Content: "re", ThreadRoot: "root-1"},
			"/channels/dm-1/messages"},
	}
	for _, c := range cases {
		rec := &recorded{}
		srv := newRecordingDiscordServer(t, rec)
		restore := mockAllDiscordEndpoints(srv.URL)
		b := &bot{session: newTestSession(t)}
		if _, err := b.Send(c.req); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		for _, r := range rec.snapshot() {
			if strings.HasSuffix(r.Path, "/threads") {
				t.Errorf("%s: unexpected thread start: %+v", c.name, r)
			}
		}
		assertPath(t, rec, "POST", c.path)
		restore()
		srv.Close()
	}
}

func TestThreadName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"short answer\nrest", "short answer"},
		{"  padded  ", "padded"},
		{"", "thread"},
		{strings.Repeat("é", 150), strings.Repeat("é", 100)},
	}
	for _, c := range cases {
		if got := threadName(c.in); got != c.want {
			t.Errorf("threadName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
