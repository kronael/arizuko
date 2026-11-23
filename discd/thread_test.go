package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/retry"
)

// newThreadDiscordServer mimics the two REST calls the reply-into-thread path
// makes: POST .../messages/<root>/threads (start thread → channel root-1) and
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
	srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"root-1"}`)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	id, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "answer\nmore",
		ReplyTo: "root-1", ThreadRoot: "root-1", Title: "Chosen title with all five words",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "msg-ok" {
		t.Errorf("id = %q", id)
	}
	assertPath(t, rec, "POST", "/channels/ch-1/messages/root-1/threads")
	assertPath(t, rec, "POST", "/channels/root-1/messages")
	for _, r := range rec.snapshot() {
		if strings.HasSuffix(r.Path, "/threads") {
			var ts struct {
				Name string `json:"name"`
			}
			json.Unmarshal([]byte(r.Body), &ts)
			if ts.Name != "🤖 Chosen title with all five words" {
				t.Errorf("thread name = %q, want the explicit title", ts.Name)
			}
		}
		if r.Path == "/channels/root-1/messages" && strings.Contains(r.Body, "message_reference") {
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
		ChatJID: "discord:g1/ch-1", Content: "again", ThreadRoot: "root-1", Title: "Chosen title",
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

func TestSendThreadTitle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"  padded  ", "🤖 padded"},
		{"🤖 ack — done", "🤖 ack — done"},
		{"Lending risk is the fee only", "🤖 Lending risk is the fee only"},
		{strings.Repeat("é", 150), "🤖 " + strings.Repeat("é", 98)},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			rec := &recorded{}
			srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"root-1"}`)
			defer srv.Close()
			defer mockAllDiscordEndpoints(srv.URL)()
			b := &bot{session: newTestSession(t)}
			if _, err := b.Send(chanlib.SendRequest{
				ChatJID: "discord:g1/ch-1", Content: "unrelated reply words",
				ThreadRoot: "root-1", Title: c.in,
			}); err != nil {
				t.Fatal(err)
			}
			var start struct{ Name string }
			if err := json.Unmarshal([]byte(rec.snapshot()[0].Body), &start); err != nil {
				t.Fatal(err)
			}
			if start.Name != c.want {
				t.Errorf("name = %q, want %q", start.Name, c.want)
			}
		})
	}
}

func TestSendTitleRenamesExistingThread(t *testing.T) {
	for _, c := range []struct{ title, want string }{{"Server decommission", "🤖 Server decommission"}, {"", ""}} {
		rec := &recorded{}
		srv := newThreadDiscordServer(t, rec, http.StatusOK, `{"id":"th-1"}`)
		defer mockAllDiscordEndpoints(srv.URL)()
		b := &bot{session: newTestSession(t)}
		if _, err := b.Send(chanlib.SendRequest{ChatJID: "discord:g1/ch-1", Content: "x", ThreadID: "th-1", Title: c.title}); err != nil {
			t.Fatal(err)
		}
		var renames []string
		for _, r := range rec.snapshot() {
			if r.Method == http.MethodPatch && strings.HasSuffix(r.Path, "/channels/th-1") {
				var edit struct{ Name string }
				_ = json.Unmarshal([]byte(r.Body), &edit)
				renames = append(renames, edit.Name)
			}
		}
		if c.want == "" && len(renames) != 0 || c.want != "" && (len(renames) != 1 || renames[0] != c.want) {
			t.Errorf("title %q: renames = %q, want %q", c.title, renames, c.want)
		}
		srv.Close()
	}
}

func TestUntitledThreadDelivery(t *testing.T) {
	for _, kind := range []string{"text", "file", "voice"} {
		for _, existing := range []bool{false, true} {
			t.Run(kind+map[bool]string{false: "/missing", true: "/existing"}[existing], func(t *testing.T) {
				rec := &recorded{}
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					rec.record(r.Method, r.URL.Path, "")
					if kind == "voice" && r.Method == http.MethodPost {
						if err := r.ParseMultipartForm(1 << 20); err != nil {
							t.Error(err)
						} else if files := r.MultipartForm.File["files[0]"]; len(files) != 1 ||
							files[0].Header.Get("Content-Type") != "audio/ogg" {
							t.Errorf("voice upload = %v, want audio/ogg", files)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if r.Method == http.MethodGet && r.URL.Path == "/channels/root-1" {
						if existing {
							_, _ = w.Write([]byte(`{"id":"root-1","type":11,"parent_id":"ch-1"}`))
						} else {
							w.WriteHeader(http.StatusNotFound)
							_, _ = w.Write([]byte(`{"code":10003,"message":"Unknown Channel"}`))
						}
						return
					}
					_, _ = w.Write([]byte(`{"id":"msg-ok"}`))
				}))
				defer srv.Close()
				defer mockAllDiscordEndpoints(srv.URL)()
				b := &bot{session: newTestSession(t)}
				path := filepath.Join(t.TempDir(), "result.txt")
				if err := os.WriteFile(path, []byte("result"), 0600); err != nil {
					t.Fatal(err)
				}
				var id string
				var err error
				switch kind {
				case "text":
					id, err = b.Send(chanlib.SendRequest{ChatJID: "discord:g1/ch-1", Content: "content cannot name a thread", ThreadRoot: "root-1"})
				case "file":
					id, err = b.SendFile("discord:g1/ch-1", path, "result.txt", "caption cannot name a thread", "", "", "root-1", "")
				case "voice":
					id, err = b.SendVoice("discord:g1/ch-1", path, "caption cannot name a thread", "", "root-1", "")
				}
				if existing {
					if err != nil || id != "msg-ok" {
						t.Fatalf("reuse: id=%q err=%v", id, err)
					}
					assertPath(t, rec, "POST", "/channels/root-1/messages")
				} else if id != "" || !errors.Is(err, chanlib.ErrPermanent) ||
					!strings.Contains(err.Error(), "pass title") || retry.FromError(err, time.Now()).Kind != "permanent" {
					t.Fatalf("refusal: id=%q err=%v", id, err)
				}
				posts := 0
				for _, request := range rec.snapshot() {
					if strings.HasSuffix(request.Path, "/threads") {
						t.Fatal("untitled delivery started a thread")
					}
					if request.Method == http.MethodPost {
						posts++
					}
				}
				assertPath(t, rec, "GET", "/channels/root-1")
				if !existing && posts != 0 {
					t.Fatalf("refused delivery posted %d requests", posts)
				}
			})
		}
	}
}

func TestSendThreadFailureNeverPostsAtParent(t *testing.T) {
	rec := &recorded{}
	srv := newThreadDiscordServer(t, rec, http.StatusForbidden, `{"message":"Missing Permissions","code":50013}`)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()
	b := &bot{session: newTestSession(t)}
	if id, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "the result", ThreadRoot: "root-1", Title: "Chosen title",
	}); err == nil || id != "" {
		t.Fatalf("thread failure must refuse delivery: id=%q err=%v", id, err)
	}
	for _, request := range rec.snapshot() {
		if request.Path == "/channels/ch-1/messages" {
			t.Fatal("child escaped to parent after thread creation failed")
		}
	}
}
