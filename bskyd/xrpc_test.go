package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
)

// bskyMock models a minimal Bluesky server sufficient to exercise the
// xrpc/auth/send/poll paths. All routes are in-process httptest handlers;
// no real network calls.
type bskyMock struct {
	mu            sync.Mutex
	srv           *httptest.Server
	createHits    int32
	refreshHits   int32
	listHits      int32
	updateSeenHits int32
	getRecordHits int32
	createRecords []map[string]any
	accessJwt     string
	refreshJwt    string
	// notifications returned by listNotifications (newest first)
	notifications []notification
	// behaviour toggles
	rejectAccess bool // force 401 on auth-required routes
	refreshFails bool
}

func newBskyMock() *bskyMock {
	m := &bskyMock{
		accessJwt:  "access-1",
		refreshJwt: "refresh-1",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.createHits, 1)
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["identifier"] == "bad" {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"invalid"}`))
			return
		}
		json.NewEncoder(w).Encode(session{
			DID: "did:plc:test", AccessJwt: m.accessJwt, RefreshJwt: m.refreshJwt,
		})
	})
	mux.HandleFunc("/xrpc/com.atproto.server.refreshSession", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.refreshHits, 1)
		m.mu.Lock()
		fails := m.refreshFails
		m.mu.Unlock()
		if fails {
			w.WriteHeader(401)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "+m.refreshJwt) {
			w.WriteHeader(401)
			return
		}
		// rotate tokens so caller observes the new access token
		m.mu.Lock()
		m.accessJwt = "access-2"
		m.refreshJwt = "refresh-2"
		m.mu.Unlock()
		json.NewEncoder(w).Encode(session{
			DID: "did:plc:test", AccessJwt: m.accessJwt, RefreshJwt: m.refreshJwt,
		})
	})
	mux.HandleFunc("/xrpc/app.bsky.notification.listNotifications", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.listHits, 1)
		m.mu.Lock()
		reject := m.rejectAccess
		tok := m.accessJwt
		ns := append([]notification(nil), m.notifications...)
		m.mu.Unlock()
		if reject || r.Header.Get("Authorization") != "Bearer "+tok {
			w.WriteHeader(401)
			w.Write([]byte(`{"error":"ExpiredToken"}`))
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"notifications": ns})
	})
	mux.HandleFunc("/xrpc/app.bsky.notification.updateSeen", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.updateSeenHits, 1)
		w.Write([]byte(`{}`))
	})
	mux.HandleFunc("/xrpc/com.atproto.repo.getRecord", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&m.getRecordHits, 1)
		rkey := r.URL.Query().Get("rkey")
		json.NewEncoder(w).Encode(map[string]string{"cid": "cid-for-" + rkey})
	})
	mux.HandleFunc("/xrpc/com.atproto.repo.createRecord", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		m.mu.Lock()
		m.createRecords = append(m.createRecords, body)
		m.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"uri": "at://did/app.bsky.feed.post/rkey"})
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *bskyMock) close() { m.srv.Close() }

func (m *bskyMock) setRejectAccess(v bool)  { m.mu.Lock(); m.rejectAccess = v; m.mu.Unlock() }
func (m *bskyMock) setRefreshFails(v bool)  { m.mu.Lock(); m.refreshFails = v; m.mu.Unlock() }
func (m *bskyMock) setNotifications(ns []notification) {
	m.mu.Lock(); m.notifications = ns; m.mu.Unlock()
}

func newTestClient(t *testing.T, m *bskyMock) *bskyClient {
	t.Helper()
	return &bskyClient{
		cfg: config{
			Service:    m.srv.URL,
			Identifier: "user",
			Password:   "pass",
			DataDir:    t.TempDir(),
			ListenURL:  "http://bskyd:9005",
		},
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestCreateSession(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)

	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	s := bc.getSession()
	if s.DID != "did:plc:test" || s.AccessJwt != "access-1" {
		t.Errorf("session = %+v", s)
	}
	// session persisted to disk
	if bc.loadSession() == nil {
		t.Error("session not persisted")
	}
}

func TestCreateSession_BadCreds(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	bc.cfg.Identifier = "bad"
	err := bc.createSession()
	if err == nil {
		t.Fatal("expected error")
	}
	if !isHTTPStatus(err, 401) {
		t.Errorf("want 401, got %v", err)
	}
}

func TestRefreshSession(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	if err := bc.refreshSession(bc.getSession().RefreshJwt); err != nil {
		t.Fatal(err)
	}
	if bc.getSession().AccessJwt != "access-2" {
		t.Errorf("access jwt not rotated: %q", bc.getSession().AccessJwt)
	}
}

func TestRefreshSession_BadToken(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	err := bc.refreshSession("wrong")
	if err == nil || !isHTTPStatus(err, 401) {
		t.Errorf("want 401, got %v", err)
	}
}

func TestAuth_UsesSavedSessionThenRefresh(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	// seed a session file so loadSession returns it
	bc.storeSession(session{DID: "did:plc:test", AccessJwt: "access-1", RefreshJwt: "refresh-1"})

	// auth path: loadSession succeeds, refreshSession succeeds, no createSession
	if err := bc.auth(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&m.createHits) != 0 {
		t.Error("createSession should not be called when refresh works")
	}
	if atomic.LoadInt32(&m.refreshHits) == 0 {
		t.Error("refreshSession should be called")
	}
}

func TestAuth_FallsBackToCreate(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	// no saved session — auth goes straight to createSession
	if err := bc.auth(); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&m.createHits) != 1 {
		t.Errorf("createHits = %d, want 1", atomic.LoadInt32(&m.createHits))
	}
}

func TestXRPC_RetryOn401WithRefresh(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	// Server will 401 on list until we refresh (which rotates access).
	// Simulate expiry: initial access token is "access-1". The mock checks
	// against its current m.accessJwt — so rotate the server's accepted
	// token to force a 401, then the client's refresh will re-sync.
	m.mu.Lock()
	m.accessJwt = "access-new"
	m.mu.Unlock()

	var result struct {
		Notifications []notification `json:"notifications"`
	}
	// refresh will update server-side tokens to "access-2" which the client
	// then holds — but the server expects whatever the refresh handler set.
	// The refresh handler rotates both server and responds with the new
	// tokens, so after retry the client's token matches.
	err := bc.xrpc("GET", "app.bsky.notification.listNotifications",
		map[string]string{"limit": "25"}, nil, &result)
	if err != nil {
		t.Fatalf("xrpc retry failed: %v", err)
	}
	if atomic.LoadInt32(&m.refreshHits) == 0 {
		t.Error("refresh should have been called")
	}
	// list should have been called at least twice (once 401, once ok)
	if atomic.LoadInt32(&m.listHits) < 2 {
		t.Errorf("listHits = %d, want >= 2", atomic.LoadInt32(&m.listHits))
	}
}

func TestXRPC_401AndRefreshFailsTriesCreate(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	// Make refresh always fail; then the code path calls createSession.
	// Force the server to reject current access so we take the 401 branch.
	m.setRejectAccess(true)
	m.setRefreshFails(true)

	// After re-auth via createSession, rejectAccess is still true, so the
	// retried xrpc still fails. We only assert: refresh attempted, create
	// attempted (cerr branch).
	err := bc.xrpc("GET", "app.bsky.notification.listNotifications", nil, nil, nil)
	if err == nil {
		t.Fatal("expected terminal error")
	}
	if atomic.LoadInt32(&m.refreshHits) == 0 {
		t.Error("refresh should have been attempted")
	}
	// createSession is re-called as fallback
	if atomic.LoadInt32(&m.createHits) < 2 {
		t.Errorf("createHits = %d, want >= 2", atomic.LoadInt32(&m.createHits))
	}
}

func TestSend(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}

	_, err := bc.Send(chanlib.SendRequest{ChatJID: "bluesky:x", Content: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.createRecords) != 1 {
		t.Fatalf("createRecords = %d", len(m.createRecords))
	}
	rec, _ := m.createRecords[0]["record"].(map[string]any)
	if rec["text"] != "hello world" {
		t.Errorf("text = %v", rec["text"])
	}
	if _, isReply := rec["reply"]; isReply {
		t.Error("non-reply send should not have reply field")
	}
}

func TestSend_WithReply(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}

	replyURI := "at://did:plc:other/app.bsky.feed.post/rkeyABC"
	_, err := bc.Send(chanlib.SendRequest{
		ChatJID: "bluesky:x", Content: "reply text", ReplyTo: replyURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&m.getRecordHits) != 1 {
		t.Errorf("getRecordHits = %d, want 1", atomic.LoadInt32(&m.getRecordHits))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.createRecords[0]["record"].(map[string]any)
	reply, ok := rec["reply"].(map[string]any)
	if !ok {
		t.Fatalf("reply field missing")
	}
	parent := reply["parent"].(map[string]any)
	if parent["uri"] != replyURI {
		t.Errorf("parent.uri = %v", parent["uri"])
	}
	if parent["cid"] != "cid-for-rkeyABC" {
		t.Errorf("parent.cid = %v", parent["cid"])
	}
}

func TestSend_InvalidReplyURI(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	_, err := bc.Send(chanlib.SendRequest{
		ChatJID: "bluesky:x", Content: "x", ReplyTo: "bad",
	})
	if err == nil {
		t.Fatal("expected error for invalid uri")
	}
}

func TestHTTPStatusError(t *testing.T) {
	e := &httpStatusError{Code: 401, Body: "bad", Op: "test"}
	if e.Error() == "" {
		t.Error("empty error string")
	}
	if !isHTTPStatus(e, 401) {
		t.Error("expected 401 match")
	}
	if isHTTPStatus(e, 500) {
		t.Error("500 should not match 401")
	}
	if isHTTPStatus(errors.New("plain"), 401) {
		t.Error("plain error should not match")
	}
	if isHTTPStatus(nil, 401) {
		t.Error("nil should not match")
	}
}

func TestTypingNoop(t *testing.T) {
	bc := &bskyClient{}
	bc.Typing("bluesky:x", true) // should not panic
}

// TestFetchNotifications_Dispatches verifies that unread notifications are
// handed to the router in oldest-first order and updateSeen is called per-item.
func TestFetchNotifications_Dispatches(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	// Notifications newest-first in API response
	m.setNotifications([]notification{
		{
			URI: "at://did:plc:a/app.bsky.feed.post/new",
			Reason: "mention", IsRead: false,
			IndexedAt: "2026-04-17T10:00:00Z",
			Author: struct {
				DID         string `json:"did"`
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
			}{DID: "did:plc:a", Handle: "alice.bsky", DisplayName: "Alice"},
			Record: struct {
				Text  string `json:"text"`
				Type  string `json:"$type"`
				Reply *struct {
					Parent struct {
						URI string `json:"uri"`
					} `json:"parent"`
				} `json:"reply,omitempty"`
				Embed *embedRecord `json:"embed,omitempty"`
			}{Text: "hello newer"},
		},
		{
			URI: "at://did:plc:a/app.bsky.feed.post/old", Reason: "reply",
			IsRead: false, IndexedAt: "2026-04-17T09:00:00Z",
			Author: struct {
				DID         string `json:"did"`
				Handle      string `json:"handle"`
				DisplayName string `json:"displayName"`
			}{DID: "did:plc:a", Handle: "alice.bsky", DisplayName: "Alice"},
			Record: struct {
				Text  string `json:"text"`
				Type  string `json:"$type"`
				Reply *struct {
					Parent struct {
						URI string `json:"uri"`
					} `json:"parent"`
				} `json:"reply,omitempty"`
				Embed *embedRecord `json:"embed,omitempty"`
			}{Text: "hello older"},
		},
		{
			URI: "at://did:plc:a/app.bsky.feed.post/read", Reason: "mention",
			IsRead: true, IndexedAt: "2026-04-17T08:00:00Z",
		},
	})

	// fake router
	mr := newBskyRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	if err := bc.fetchNotifications(rc); err != nil {
		t.Fatal(err)
	}
	// Should have dispatched 2 unread items in oldest→newest order
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 2 {
		t.Fatalf("dispatched %d, want 2", len(mr.msgs))
	}
	if mr.msgs[0].Content != "hello older" {
		t.Errorf("first dispatched = %q, want oldest first", mr.msgs[0].Content)
	}
	if mr.msgs[0].Verb != "reply" {
		t.Errorf("verb = %q, want reply", mr.msgs[0].Verb)
	}
	if mr.msgs[1].Verb != "message" {
		t.Errorf("verb = %q, want message", mr.msgs[1].Verb)
	}
	if atomic.LoadInt32(&m.updateSeenHits) != 2 {
		t.Errorf("updateSeenHits = %d, want 2", atomic.LoadInt32(&m.updateSeenHits))
	}
}

func TestHandleNotification_WithImages(t *testing.T) {
	mr := newBskyRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	bc := &bskyClient{cfg: config{ListenURL: "http://bskyd:9005"}}
	n := notification{
		URI: "at://did:plc:a/app.bsky.feed.post/xyz",
		Reason: "mention", IndexedAt: "2026-04-17T10:00:00Z",
	}
	n.Author.DID = "did:plc:a"
	n.Author.Handle = "alice.bsky"
	n.Record.Text = "check this"
	n.Record.Embed = &embedRecord{
		Type: "app.bsky.embed.images",
		Images: []embedImage{{
			Image: blobRef{Ref: blobRefLink{Link: "bafkrei1"}, MimeType: "image/jpeg", Size: 1000},
		}},
	}
	bc.handleNotification(n, rc)

	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 1 {
		t.Fatalf("dispatched %d", len(mr.msgs))
	}
	msg := mr.msgs[0]
	if !strings.Contains(msg.Content, "check this") {
		t.Errorf("content = %q", msg.Content)
	}
	if !strings.Contains(msg.Content, "[Image:") {
		t.Errorf("content should mention image: %q", msg.Content)
	}
	if len(msg.Attachments) != 1 {
		t.Errorf("attachments = %d", len(msg.Attachments))
	}
	if msg.SenderName != "alice.bsky" { // no DisplayName → handle
		t.Errorf("sender = %q", msg.SenderName)
	}
}

func TestPoll_CancelsOnContext(t *testing.T) {
	m := newBskyMock()
	defer m.close()
	bc := newTestClient(t, m)
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}

	mr := newBskyRouterMock()
	defer mr.close()
	rc := chanlib.NewRouterClient(mr.srv.URL, "")
	rc.SetToken("tok")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		bc.poll(ctx, rc)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not exit on ctx cancel")
	}
}

// minimal router mock: accepts /v1/messages for SendMessage
type bskyRouter struct {
	mu   sync.Mutex
	msgs []chanlib.InboundMsg
	srv  *httptest.Server
}

func newBskyRouterMock() *bskyRouter {
	m := &bskyRouter{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var im chanlib.InboundMsg
		json.NewDecoder(r.Body).Decode(&im)
		m.mu.Lock()
		m.msgs = append(m.msgs, im)
		m.mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	})
	m.srv = httptest.NewServer(mux)
	return m
}

func (m *bskyRouter) close() { m.srv.Close() }

func TestFetchHistory(t *testing.T) {
	var capturedQuery string
	var pageHits int32
	mux := http.NewServeMux()
	mux.HandleFunc("/xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(session{DID: "did:plc:me", AccessJwt: "a", RefreshJwt: "r"})
	})
	mux.HandleFunc("/xrpc/app.bsky.feed.getAuthorFeed", func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&pageHits, 1)
		capturedQuery = r.URL.RawQuery
		// Return two posts, newest first; empty cursor → no more pages.
		w.Write([]byte(`{
			"feed":[
				{"post":{"uri":"at://did:plc:a/app.bsky.feed.post/p2","author":{"did":"did:plc:a","handle":"alice.bsky","displayName":"Alice"},"indexedAt":"2026-04-18T12:00:00Z","record":{"text":"second"}}},
				{"post":{"uri":"at://did:plc:a/app.bsky.feed.post/p1","author":{"did":"did:plc:a","handle":"alice.bsky","displayName":"Alice"},"indexedAt":"2026-04-18T11:00:00Z","record":{"text":"first","reply":{"parent":{"uri":"at://did:plc:a/app.bsky.feed.post/p0"}}}}}
			]
		}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bc := &bskyClient{
		cfg:  config{Service: srv.URL, Identifier: "u", Password: "p", DataDir: t.TempDir()},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	resp, err := bc.FetchHistory(chanlib.HistoryRequest{
		ChatJID: "bluesky:did:plc:a", Limit: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "platform" {
		t.Errorf("source = %q", resp.Source)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("got %d messages", len(resp.Messages))
	}
	if resp.Messages[0].ID != "p2" || resp.Messages[0].Content != "second" {
		t.Errorf("msg[0] = %+v", resp.Messages[0])
	}
	if resp.Messages[1].ReplyTo != "p0" {
		t.Errorf("msg[1] replyTo = %q", resp.Messages[1].ReplyTo)
	}
	if resp.Messages[1].Sender != "bluesky:user/did%3Aplc%3Aa" || resp.Messages[1].SenderName != "Alice" {
		t.Errorf("sender = %s / %s", resp.Messages[1].Sender, resp.Messages[1].SenderName)
	}
	if !strings.Contains(capturedQuery, "actor=did") {
		t.Errorf("query missing actor: %q", capturedQuery)
	}
	if !strings.Contains(capturedQuery, "limit=50") {
		t.Errorf("query missing limit: %q", capturedQuery)
	}
	if atomic.LoadInt32(&pageHits) == 0 {
		t.Error("feed endpoint never hit")
	}
}

func TestFetchHistory_BeforeFilter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/xrpc/com.atproto.server.createSession", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(session{DID: "did:plc:me", AccessJwt: "a", RefreshJwt: "r"})
	})
	mux.HandleFunc("/xrpc/app.bsky.feed.getAuthorFeed", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"feed":[
			{"post":{"uri":"at://x/p/new","author":{"did":"did:plc:a","handle":"a"},"indexedAt":"2026-04-20T00:00:00Z","record":{"text":"new"}}},
			{"post":{"uri":"at://x/p/old","author":{"did":"did:plc:a","handle":"a"},"indexedAt":"2026-04-10T00:00:00Z","record":{"text":"old"}}}
		]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	bc := &bskyClient{
		cfg:  config{Service: srv.URL, Identifier: "u", Password: "p", DataDir: t.TempDir()},
		http: &http.Client{Timeout: 5 * time.Second},
	}
	if err := bc.createSession(); err != nil {
		t.Fatal(err)
	}
	before, _ := time.Parse(time.RFC3339, "2026-04-15T00:00:00Z")
	resp, err := bc.FetchHistory(chanlib.HistoryRequest{
		ChatJID: "bluesky:did:plc:a", Limit: 50, Before: before,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Messages) != 1 || resp.Messages[0].Content != "old" {
		t.Errorf("messages = %+v (Before filter should drop 'new')", resp.Messages)
	}
}

func TestFetchHistory_Interface(t *testing.T) {
	var _ chanlib.HistoryProvider = (*bskyClient)(nil)
}
