package chanreg

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// The verb surface is the wire contract between routd and every adapter. The
// capability gate on each verb is tested (verb_test.go) and so is the 501 and
// non-200 handling — but nothing checked WHAT is sent. A verb posting to the
// wrong path errors visibly; a verb sending the wrong JSON key does not: the
// adapter reads an absent field, acts on an empty string, and answers 200.
// That is a like on nothing, an edit of nothing, a forward to nowhere.

type recorded struct {
	path        string
	method      string
	contentType string
	auth        string
	body        map[string]any
}

// stubAdapter records one request and answers 200 with an id.
func stubAdapter(t *testing.T, got *recorded) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.method = r.URL.Path, r.Method
		got.contentType = r.Header.Get("Content-Type")
		got.auth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		got.body = map[string]any{}
		_ = json.Unmarshal(raw, &got.body)
		_, _ = io.WriteString(w, `{"id":"posted-1"}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func chanWithCaps(t *testing.T, url string, caps ...string) *HTTPChannel {
	t.Helper()
	m := map[string]bool{}
	for _, c := range caps {
		m[c] = true
	}
	return NewHTTPChannel(&Entry{
		Name: "stub", URL: url, JIDPrefixes: []string{"x:"}, Capabilities: m,
	}, staticBearer("service-token"))
}

// One row per verb: the endpoint it must reach and the exact body keys the
// adapter reads. A missing key here is a silent no-op in production.
func TestEveryVerbPostsItsDocumentedPathAndBody(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		verb     string
		cap      string
		call     func(*HTTPChannel) error
		wantPath string
		wantBody map[string]any
	}{
		{
			verb: "post", cap: "post", wantPath: "/post",
			call:     func(h *HTTPChannel) error { _, err := h.Post(ctx, "x:1", "hello", nil); return err },
			wantBody: map[string]any{"chat_jid": "x:1", "content": "hello"},
		},
		{
			verb: "post with media", cap: "post", wantPath: "/post",
			call: func(h *HTTPChannel) error {
				_, err := h.Post(ctx, "x:1", "hello", []string{"/a.png", "/b.png"})
				return err
			},
			wantBody: map[string]any{
				"chat_jid": "x:1", "content": "hello",
				"media_paths": []any{"/a.png", "/b.png"},
			},
		},
		{
			verb: "like", cap: "like", wantPath: "/like",
			call:     func(h *HTTPChannel) error { return h.Like(ctx, "x:1", "m1", "👍") },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1", "reaction": "👍"},
		},
		{
			verb: "dislike", cap: "dislike", wantPath: "/dislike",
			call:     func(h *HTTPChannel) error { return h.Dislike(ctx, "x:1", "m1") },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1"},
		},
		{
			verb: "delete", cap: "delete", wantPath: "/delete",
			call:     func(h *HTTPChannel) error { return h.Delete(ctx, "x:1", "m1") },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1"},
		},
		{
			verb: "edit", cap: "edit", wantPath: "/edit",
			call:     func(h *HTTPChannel) error { return h.Edit(ctx, "x:1", "m1", "new text", "") },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1", "content": "new text"},
		},
		{
			verb: "thread edit", cap: "edit", wantPath: "/edit",
			call: func(h *HTTPChannel) error {
				return h.Edit(ctx, "x:1", "m1", "new text", "th-7")
			},
			wantBody: map[string]any{
				"chat_jid": "x:1", "target_id": "m1", "content": "new text", "thread_id": "th-7",
			},
		},
		{
			verb: "pin", cap: "pin", wantPath: "/pin",
			call:     func(h *HTTPChannel) error { return h.Pin(ctx, "x:1", "m1") },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1"},
		},
		{
			verb: "unpin one", cap: "pin", wantPath: "/unpin",
			call:     func(h *HTTPChannel) error { return h.Unpin(ctx, "x:1", "m1", false) },
			wantBody: map[string]any{"chat_jid": "x:1", "target_id": "m1"},
		},
		{
			// all=true drops target_id entirely: sending both would let an
			// adapter that reads target_id first unpin one message and report
			// success for "unpin everything".
			verb: "unpin all", cap: "pin", wantPath: "/unpin",
			call:     func(h *HTTPChannel) error { return h.Unpin(ctx, "x:1", "m1", true) },
			wantBody: map[string]any{"chat_jid": "x:1", "all": true},
		},
		{
			verb: "forward", cap: "fwd", wantPath: "/forward",
			call:     func(h *HTTPChannel) error { _, err := h.Forward(ctx, "m1", "x:2", "look"); return err },
			wantBody: map[string]any{"source_msg_id": "m1", "target_jid": "x:2", "comment": "look"},
		},
		{
			verb: "forward without a comment", cap: "fwd", wantPath: "/forward",
			call:     func(h *HTTPChannel) error { _, err := h.Forward(ctx, "m1", "x:2", ""); return err },
			wantBody: map[string]any{"source_msg_id": "m1", "target_jid": "x:2"},
		},
		{
			verb: "quote", cap: "quote", wantPath: "/quote",
			call:     func(h *HTTPChannel) error { _, err := h.Quote(ctx, "x:1", "m1", "this"); return err },
			wantBody: map[string]any{"chat_jid": "x:1", "source_msg_id": "m1", "comment": "this"},
		},
		{
			verb: "repost", cap: "repost", wantPath: "/repost",
			call:     func(h *HTTPChannel) error { _, err := h.Repost(ctx, "x:1", "m1"); return err },
			wantBody: map[string]any{"chat_jid": "x:1", "source_msg_id": "m1"},
		},
	}

	for _, c := range cases {
		t.Run(c.verb, func(t *testing.T) {
			var got recorded
			srv := stubAdapter(t, &got)
			if err := c.call(chanWithCaps(t, srv.URL, c.cap)); err != nil {
				t.Fatalf("%s: %v", c.verb, err)
			}
			if got.path != c.wantPath {
				t.Errorf("posted to %q, want %q", got.path, c.wantPath)
			}
			if got.method != http.MethodPost {
				t.Errorf("method = %q, want POST", got.method)
			}
			if got.contentType != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", got.contentType)
			}
			if got.auth != "Bearer service-token" {
				t.Errorf("Authorization = %q; the adapter would 401", got.auth)
			}
			if !reflect.DeepEqual(got.body, c.wantBody) {
				t.Errorf("body mismatch\n got %#v\nwant %#v", got.body, c.wantBody)
			}
		})
	}
}

// A verb that returns an id must return the adapter's, not an empty string:
// the id is what a later edit, delete or like targets.
func TestVerbsReturnTheAdapterID(t *testing.T) {
	ctx := context.Background()
	var got recorded
	srv := stubAdapter(t, &got)
	h := chanWithCaps(t, srv.URL, "post", "fwd", "quote", "repost")

	for name, call := range map[string]func() (string, error){
		"post":    func() (string, error) { return h.Post(ctx, "x:1", "hi", nil) },
		"forward": func() (string, error) { return h.Forward(ctx, "m1", "x:2", "") },
		"quote":   func() (string, error) { return h.Quote(ctx, "x:1", "m1", "") },
		"repost":  func() (string, error) { return h.Repost(ctx, "x:1", "m1") },
	} {
		id, err := call()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if id != "posted-1" {
			t.Errorf("%s returned id %q, want the adapter's", name, id)
		}
	}
}

// round_done, history, pane verbs

// The body key is chat_jid and the value is the turn's chat JID verbatim.
// Keying round_done on anything else — the route's target folder, a derived
// value — leaves the browser waiting forever (BUGS L30, and again in webd).
func TestPostRoundDoneSendsTheChatJIDVerbatim(t *testing.T) {
	var got recorded
	srv := stubAdapter(t, &got)
	h := chanWithCaps(t, srv.URL)

	if err := h.PostRoundDone("web:acme/support#thread-7", "turn-1", "ok", ""); err != nil {
		t.Fatalf("PostRoundDone: %v", err)
	}
	if got.path != "/v1/round_done" {
		t.Errorf("path = %q", got.path)
	}
	want := map[string]any{
		"chat_jid": "web:acme/support#thread-7", "turn_id": "turn-1", "status": "ok",
	}
	if !reflect.DeepEqual(got.body, want) {
		t.Errorf("body\n got %#v\nwant %#v", got.body, want)
	}

	// An error message rides along only when there is one.
	if err := h.PostRoundDone("x:1", "turn-2", "error", "boom"); err != nil {
		t.Fatal(err)
	}
	if got.body["error"] != "boom" {
		t.Errorf("error field = %v, want boom", got.body["error"])
	}
}

// An adapter that predates the round-handle protocol answers 404. That is
// "not implemented", not a failure — treating it as one would make every turn
// on such an adapter log an error it can do nothing about.
func TestPostRoundDoneTolerates404AndRejectsOtherFailures(t *testing.T) {
	for status, wantErr := range map[int]bool{
		http.StatusOK:                  false,
		http.StatusNotFound:            false,
		http.StatusInternalServerError: true,
		http.StatusBadRequest:          true,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		err := chanWithCaps(t, srv.URL).PostRoundDone("x:1", "t", "ok", "")
		if gotErr := err != nil; gotErr != wantErr {
			t.Errorf("status %d: err = %v, want error=%v", status, err, wantErr)
		}
		srv.Close()
	}
}

func TestFetchHistoryNeedsTheCapAndBuildsTheQuery(t *testing.T) {
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `[{"id":1}]`)
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	if _, err := chanWithCaps(t, srv.URL).FetchHistory(ctx, "x:1", time.Time{}, 50); err == nil {
		t.Error("FetchHistory ran without the fetch_history capability")
	}

	h := chanWithCaps(t, srv.URL, "fetch_history")
	body, err := h.FetchHistory(ctx, "x:1", time.Time{}, 50)
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if string(body) != `[{"id":1}]` {
		t.Errorf("body = %q, want the adapter's payload verbatim", body)
	}
	if !strings.Contains(query, "jid=x:1") || !strings.Contains(query, "limit=50") {
		t.Errorf("query = %q, want jid and limit", query)
	}
	if strings.Contains(query, "before=") {
		t.Errorf("a zero `before` must be omitted, got %q", query)
	}

	before := time.Date(2026, 8, 27, 10, 30, 0, 0, time.UTC)
	if _, err := h.FetchHistory(ctx, "x:1", before, 10); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(query, "before=2026-08-27T10:30:00Z") {
		t.Errorf("query = %q, want an RFC3339 UTC before", query)
	}
}

func TestFetchHistoryReportsANon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	if _, err := chanWithCaps(t, srv.URL, "fetch_history").
		FetchHistory(context.Background(), "x:1", time.Time{}, 10); err == nil {
		t.Error("a 502 from the history endpoint read as an empty history")
	}
}

// The pane verbs are Slack-only today. An adapter with no pane answers 404,
// which must become ErrUnsupported so the agent gets "this platform cannot do
// that" instead of a generic failure it will retry.
func TestPaneVerbsMapA404ToUnsupported(t *testing.T) {
	ctx := context.Background()
	prompts := []core.PanePrompt{{Title: "Retry", Message: "try again"}}

	var got recorded
	ok := stubAdapter(t, &got)
	h := chanWithCaps(t, ok.URL)

	if err := h.SetSuggestions(ctx, "x:1", prompts); err != nil {
		t.Fatalf("SetSuggestions: %v", err)
	}
	if got.path != "/v1/pane/prompts" {
		t.Errorf("suggestions path = %q", got.path)
	}
	if got.body["jid"] != "x:1" {
		t.Errorf("suggestions body = %#v, want jid", got.body)
	}
	if list, _ := got.body["prompts"].([]any); len(list) != 1 {
		t.Errorf("prompts did not survive the encode: %#v", got.body["prompts"])
	}

	if err := h.SetName(ctx, "x:1", "New title"); err != nil {
		t.Fatalf("SetName: %v", err)
	}
	if got.path != "/v1/pane/title" {
		t.Errorf("title path = %q", got.path)
	}
	// The wire field is `title`, not `name` — the method is SetName and an
	// adapter reading `name` would silently rename nothing.
	if got.body["title"] != "New title" {
		t.Errorf("title body = %#v, want title=New title", got.body)
	}

	for status, wantUnsupported := range map[int]bool{
		http.StatusNotFound:            true,
		http.StatusInternalServerError: false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		hh := chanWithCaps(t, srv.URL)
		for name, err := range map[string]error{
			"SetSuggestions": hh.SetSuggestions(ctx, "x:1", prompts),
			"SetName":        hh.SetName(ctx, "x:1", "t"),
		} {
			if err == nil {
				t.Errorf("%s: status %d produced no error", name, status)
				continue
			}
			if got := errors.Is(err, chanlib.ErrUnsupported); got != wantUnsupported {
				t.Errorf("%s status %d: Unsupported=%v, want %v (%v)", name, status, got, wantUnsupported, err)
			}
		}
		srv.Close()
	}
}

// Multipart uploads.

func TestSendVoiceUploadsTheFileAsMultipart(t *testing.T) {
	dir := t.TempDir()
	audio := filepath.Join(dir, "note.ogg")
	if err := os.WriteFile(audio, []byte("OggS-fake-audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	var (
		gotPath, gotName, gotJID, gotCaption, gotThread, gotContent string
		gotFile                                                     []byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContent = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("the body is not multipart: %v", err)
			return
		}
		gotJID = r.FormValue("chat_jid")
		gotName = r.FormValue("filename")
		gotCaption = r.FormValue("caption")
		gotThread = r.FormValue("thread_id")
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no `file` part: %v", err)
			return
		}
		defer f.Close()
		gotFile, _ = io.ReadAll(f)
		_, _ = io.WriteString(w, `{"id":"voice-1"}`)
	}))
	t.Cleanup(srv.Close)
	ctx := context.Background()

	if _, err := chanWithCaps(t, srv.URL).SendVoiceCtx(ctx, "x:1", audio, "", ""); !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("send_voice without the cap = %v, want Unsupported", err)
	}

	id, err := chanWithCaps(t, srv.URL, "send_voice").SendVoiceCtx(ctx, "x:1", audio, "listen", "thr-1")
	if err != nil {
		t.Fatalf("SendVoiceCtx: %v", err)
	}
	if id != "voice-1" {
		t.Errorf("id = %q, want the adapter's", id)
	}
	if gotPath != "/send-voice" {
		t.Errorf("path = %q", gotPath)
	}
	if mt, _, _ := mime.ParseMediaType(gotContent); mt != "multipart/form-data" {
		t.Errorf("Content-Type = %q, want multipart/form-data", gotContent)
	}
	if gotJID != "x:1" || gotCaption != "listen" || gotThread != "thr-1" {
		t.Errorf("fields: jid=%q caption=%q thread=%q", gotJID, gotCaption, gotThread)
	}
	// The filename defaults to the basename so the platform shows something
	// readable rather than the container's absolute path.
	if gotName != "note.ogg" {
		t.Errorf("filename = %q, want the basename", gotName)
	}
	if string(gotFile) != "OggS-fake-audio" {
		t.Errorf("uploaded bytes = %q, want the file's contents", gotFile)
	}
}

// A path the container cannot read must be an error before any request goes
// out — never an empty upload the platform accepts.
func TestSendVoiceRefusesAMissingFileWithoutPosting(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posted = true }))
	t.Cleanup(srv.Close)

	_, err := chanWithCaps(t, srv.URL, "send_voice").
		SendVoiceCtx(context.Background(), "x:1", filepath.Join(t.TempDir(), "gone.ogg"), "", "")
	if err == nil {
		t.Fatal("a missing audio file was uploaded as a success")
	}
	if posted {
		t.Error("the adapter was contacted for a file that could not be opened")
	}
}

func TestSendFileNeedsItsCapability(t *testing.T) {
	var got recorded
	srv := stubAdapter(t, &got)
	_, err := chanWithCaps(t, srv.URL).SendFileCtx(context.Background(), "x:1", "/tmp/x", "x", "", "", "", "")
	if err == nil {
		t.Fatal("SendFile ran without the send_file capability")
	}
	if !strings.Contains(err.Error(), "send_file") {
		t.Errorf("the error must name the missing capability, got %q", err)
	}
}

// Registry surface.

// OwnsScheme stops one adapter asserting another platform's identities. It is
// coarser than Owns on purpose: registered prefixes are CHAT prefixes
// ("telegram:mybot/") while a sender is "telegram:user/7", so the two share
// only the scheme.
func TestOwnsSchemeMatchesTheSchemeAndNothingElse(t *testing.T) {
	e := &Entry{Name: "teled", JIDPrefixes: []string{"telegram:mybot/", "telegram:group/"}}

	for _, principal := range []string{"telegram:user/7", "telegram:group/-100", "telegram:"} {
		if !e.OwnsScheme(principal) {
			t.Errorf("OwnsScheme(%q) = false; teled cannot speak for its own platform", principal)
		}
	}
	for _, principal := range []string{
		"discord:user/7",   // another platform
		"google:123",       // an OAuth sub no adapter owns
		"alice",            // no scheme at all
		"",                 // empty
		":user/7",          // an empty scheme
		"Telegram:user/7",  // case matters; a scheme is not fuzzy
		"xtelegram:user/7", // not a prefix match on the scheme
	} {
		if e.OwnsScheme(principal) {
			t.Errorf("OwnsScheme(%q) = true; teled claimed an identity that is not its own", principal)
		}
	}
}

// ByPrincipal is how a daemon-authenticated adapter request finds its own
// entry. A miss must be nil, never another adapter's entry.
func TestByPrincipalFindsOnlyTheRegisteredDaemon(t *testing.T) {
	r := New()
	if _, err := r.RegisterWithOrigin("teled", "http://10.0.0.11:8080",
		[]string{"telegram:"}, map[string]bool{"send_text": true}, "", "teled"); err != nil {
		t.Fatal(err)
	}
	if got := r.ByPrincipal("teled"); got == nil || got.Name != "teled" {
		t.Fatalf("ByPrincipal(teled) = %v, want the teled entry", got)
	}
	for _, miss := range []string{"discd", "", "service:teled", "TELED"} {
		if got := r.ByPrincipal(miss); got != nil {
			t.Errorf("ByPrincipal(%q) = %v, want nil", miss, got)
		}
	}
}

// The bearer is routd's service:routd token source. nil means local dev with
// no AUTHD_URL, and egress must then go out with NO Authorization header
// rather than an empty one, which an adapter would read as a malformed token.
func TestBearerSourceIsOptionalAndReplaceable(t *testing.T) {
	r := New()
	if r.Bearer() != nil {
		t.Error("a fresh registry must have no bearer source")
	}
	r.SetBearer(staticBearer("tok"))
	if r.Bearer() == nil {
		t.Fatal("SetBearer did not take")
	}
	if got, err := r.Bearer()(context.Background()); err != nil || got != "tok" {
		t.Errorf("Bearer() returned (%q,%v)", got, err)
	}

	var got recorded
	srv := stubAdapter(t, &got)
	h := NewHTTPChannel(&Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"},
		Capabilities: map[string]bool{"like": true}}, nil)
	if err := h.Like(context.Background(), "x:1", "m1", "👍"); err != nil {
		t.Fatal(err)
	}
	if got.auth != "" {
		t.Errorf("Authorization = %q with a nil bearer source, want the header absent", got.auth)
	}
}

// send: the full body, and the file half of trySend.

// The optional threading fields ride along only when set. An empty reply_to or
// thread_id on the wire is not the same as an absent one: an adapter that
// reads the key and threads on "" posts into a thread that does not exist.
func TestSendOmitsEmptyThreadingFields(t *testing.T) {
	var got recorded
	srv := stubAdapter(t, &got)
	h := chanWithCaps(t, srv.URL, "send_text")

	if _, err := h.SendCtx(context.Background(), "x:1", "hello", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if want := (map[string]any{"chat_jid": "x:1", "content": "hello"}); !reflect.DeepEqual(got.body, want) {
		t.Errorf("bare send body\n got %#v\nwant %#v", got.body, want)
	}

	if _, err := h.SendCtx(context.Background(), "x:1", "hello", "m0", "thr", "root", "idem-7"); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"chat_jid": "x:1", "content": "hello", "reply_to": "m0",
		"thread_id": "thr", "thread_root": "root", "idempotency_key": "idem-7",
	}
	if !reflect.DeepEqual(got.body, want) {
		t.Errorf("threaded send body\n got %#v\nwant %#v", got.body, want)
	}
	if got.path != "/send" {
		t.Errorf("path = %q, want /send", got.path)
	}
}

// The file branch of trySend goes to /send-file as multipart, carrying the
// idempotency key routd's pending sweep depends on: without it a re-send after
// a timeout posts the attachment twice.
func TestSendFileUploadsMultipartWithTheIdempotencyKey(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "report.pdf")
	if err := os.WriteFile(doc, []byte("%PDF-fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotPath, gotJID, gotName, gotCaption, gotReplyTo, gotIdem string
	var gotFile []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
			return
		}
		gotJID, gotName = r.FormValue("chat_jid"), r.FormValue("filename")
		gotCaption, gotReplyTo = r.FormValue("caption"), r.FormValue("reply_to")
		gotIdem = r.FormValue("idempotency_key")
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("no file part: %v", err)
			return
		}
		defer f.Close()
		gotFile, _ = io.ReadAll(f)
		_, _ = io.WriteString(w, `{"id":"file-1"}`)
	}))
	t.Cleanup(srv.Close)

	// The no-context wrapper is what routd's delivery path calls.
	id, err := chanWithCaps(t, srv.URL, "send_file").
		SendFile("x:1", doc, "report.pdf", "here", "m0", "", "idem-9")
	if err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	if id != "file-1" {
		t.Errorf("id = %q", id)
	}
	if gotPath != "/send-file" {
		t.Errorf("path = %q, want /send-file", gotPath)
	}
	if gotJID != "x:1" || gotName != "report.pdf" || gotCaption != "here" || gotReplyTo != "m0" {
		t.Errorf("fields: jid=%q name=%q caption=%q reply_to=%q", gotJID, gotName, gotCaption, gotReplyTo)
	}
	if gotIdem != "idem-9" {
		t.Errorf("idempotency_key = %q; the pending sweep would double-post the attachment", gotIdem)
	}
	if string(gotFile) != "%PDF-fake" {
		t.Errorf("uploaded bytes = %q", gotFile)
	}
}

// A file send that the adapter refuses must classify the status the same way
// the text path does, so routd's sweep knows whether to retry.
func TestSendFileClassifiesTheAdapterStatus(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "x.txt")
	if err := os.WriteFile(doc, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for status, wantPermanent := range map[int]bool{
		http.StatusBadRequest:          true,
		http.StatusInternalServerError: false,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := chanWithCaps(t, srv.URL, "send_file").
			SendFileCtx(context.Background(), "x:1", doc, "x.txt", "", "", "", "")
		if err == nil {
			t.Errorf("status %d: send-file reported success", status)
			srv.Close()
			continue
		}
		if got := errors.Is(err, chanlib.ErrPermanent); got != wantPermanent {
			t.Errorf("status %d: permanent=%v, want %v (%v)", status, got, wantPermanent, err)
		}
		srv.Close()
	}
}

// The no-context wrapper must reach the same endpoint as its Ctx twin.
func TestSendVoiceWrapperReachesTheSameEndpoint(t *testing.T) {
	audio := filepath.Join(t.TempDir(), "v.ogg")
	if err := os.WriteFile(audio, []byte("ogg"), 0o644); err != nil {
		t.Fatal(err)
	}
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = io.WriteString(w, `{"id":"v1"}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := chanWithCaps(t, srv.URL, "send_voice").SendVoice("x:1", audio, "", ""); err != nil {
		t.Fatalf("SendVoice: %v", err)
	}
	if path != "/send-voice" {
		t.Errorf("path = %q, want /send-voice", path)
	}
}

// The health loop.

// checkAll is the loop's whole body. An adapter that fails maxHealthFails
// consecutive probes is deregistered — that is what turns a dead adapter into
// "no route" instead of a queue of messages nobody delivers. A single success
// in between must reset the count, or a flapping adapter is dropped for one
// bad probe out of three.
func TestHealthLoopDeregistersAfterThreeConsecutiveFailures(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("health probe hit %q", r.URL.Path)
		}
		if healthy.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"disconnected"}`)
	}))
	t.Cleanup(srv.Close)

	r := New()
	if _, err := r.Register("stub", srv.URL, []string{"x:"}, map[string]bool{"send_text": true}); err != nil {
		t.Fatal(err)
	}

	r.checkAll()
	if r.Get("stub") == nil {
		t.Fatal("a healthy adapter was deregistered")
	}

	healthy.Store(false)
	r.checkAll()
	r.checkAll()
	if r.Get("stub") == nil {
		t.Fatal("deregistered after two failures; the threshold is three")
	}

	// One good probe clears the count.
	healthy.Store(true)
	r.checkAll()
	healthy.Store(false)
	r.checkAll()
	r.checkAll()
	if r.Get("stub") == nil {
		t.Fatal("a success in between did not reset the failure count")
	}

	r.checkAll()
	if r.Get("stub") != nil {
		t.Error("three consecutive failures did not deregister the adapter")
	}
}

// "stale" means the adapter is alive and the platform is quiet — not a
// failure. Counting it would deregister every idle adapter after 90 seconds.
func TestHealthPingTreatsStaleAsAlive(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		wantOK bool
	}{
		"200":         {http.StatusOK, `{"status":"ok"}`, true},
		"503 stale":   {http.StatusServiceUnavailable, `{"status":"stale"}`, true},
		"503 down":    {http.StatusServiceUnavailable, `{"status":"disconnected"}`, false},
		"500 no body": {http.StatusInternalServerError, ``, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = io.WriteString(w, c.body)
			}))
			defer srv.Close()
			err := healthPing(srv.Client(), srv.URL)
			if gotOK := err == nil; gotOK != c.wantOK {
				t.Errorf("healthPing = %v, want ok=%v", err, c.wantOK)
			}
		})
	}

	// An adapter that is not listening at all is a failure, not a pass.
	if err := healthPing(&http.Client{Timeout: time.Second}, "http://127.0.0.1:1"); err == nil {
		t.Error("a refused connection read as healthy")
	}
}

// The loop stops when its context does. A leaked ticker goroutine per registry
// would outlive every routd reload.
func TestStartHealthLoopStopsWithItsContext(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	New().StartHealthLoop(ctx)
	cancel()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Errorf("the health loop goroutine outlived its context (%d -> %d)", before, runtime.NumGoroutine())
}

// The multipart body distinguishes "no caption" from "an empty caption": the
// optional parts are ABSENT when unset, while chat_jid and filename always go
// out because every adapter reads both by name. A field-writing loop that
// emitted all six would tell the adapter the send carries an empty caption, an
// empty reply target and an empty thread.
func TestSendFileOmitsTheOptionalPartsItHasNoValueFor(t *testing.T) {
	doc := filepath.Join(t.TempDir(), "bare.txt")
	if err := os.WriteFile(doc, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	var present []string
	var filename string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("not multipart: %v", err)
			return
		}
		for k := range r.MultipartForm.Value {
			present = append(present, k)
		}
		sort.Strings(present)
		filename = r.FormValue("filename")
		_, _ = io.WriteString(w, `{"id":"f1"}`)
	}))
	t.Cleanup(srv.Close)

	if _, err := chanWithCaps(t, srv.URL, "send_file").
		SendFile("x:1", doc, "", "", "", "", ""); err != nil {
		t.Fatalf("SendFile: %v", err)
	}
	want := []string{"chat_jid", "filename"}
	if !slices.Equal(present, want) {
		t.Errorf("parts = %v, want %v", present, want)
	}
	if filename != "" {
		t.Errorf("filename = %q, want the empty value the caller passed", filename)
	}
}
