package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattn/go-mastodon"

	"github.com/kronael/arizuko/chanlib"
)

// routerMock captures inbound messages from handleNotification.
type routerMock struct {
	mu   sync.Mutex
	msgs []chanlib.InboundMsg
	srv  *httptest.Server
}

func newRouterMock() *routerMock {
	m := &routerMock{}
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

func (m *routerMock) close()   { m.srv.Close() }
func (m *routerMock) last() chanlib.InboundMsg {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		return chanlib.InboundMsg{}
	}
	return m.msgs[len(m.msgs)-1]
}

func testMastoClient(t *testing.T) *mastoClient {
	t.Helper()
	return &mastoClient{
		cfg:   config{ListenURL: "http://mastd:9004"},
		files: chanlib.NewURLCache(10),
	}
}

func testRouter(t *testing.T) (*chanlib.RouterClient, *routerMock) {
	t.Helper()
	m := newRouterMock()
	rc := chanlib.NewRouterClient(m.srv.URL, "")
	rc.SetToken("tok")
	return rc, m
}

func TestHandleNotification_Mention(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "mention",
		Account: mastodon.Account{ID: "42", DisplayName: "Alice", Acct: "alice@example"},
		Status: &mastodon.Status{
			ID:        "status-1",
			Content:   "<p>Hello @bot</p>",
			CreatedAt: time.Unix(1_700_000_000, 0),
		},
	}
	mc.handleNotification(n, rc)

	got := mr.last()
	if got.ID != "status-1" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.ChatJID != "mastodon:account/42" {
		t.Errorf("ChatJID = %q", got.ChatJID)
	}
	if got.Content != "Hello @bot" {
		t.Errorf("Content = %q (should be stripped)", got.Content)
	}
	if got.Verb != "message" {
		t.Errorf("Verb = %q", got.Verb)
	}
	if got.SenderName != "Alice" {
		t.Errorf("SenderName = %q", got.SenderName)
	}
	if got.Timestamp != 1_700_000_000 {
		t.Errorf("Timestamp = %d", got.Timestamp)
	}
}

func TestHandleNotification_Reply(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "mention", // reply detection is via InReplyToID
		Account: mastodon.Account{ID: "7", Acct: "bob"},
		Status: &mastodon.Status{
			ID:          "s2",
			Content:     "a reply",
			InReplyToID: "parent-99",
			CreatedAt:   time.Unix(1_700_000_100, 0),
		},
	}
	mc.handleNotification(n, rc)
	got := mr.last()
	if got.Topic != "parent-99" {
		t.Errorf("Topic = %q", got.Topic)
	}
	if got.Verb != "reply" {
		t.Errorf("Verb = %q, want reply", got.Verb)
	}
	// Acct used when DisplayName empty
	if got.SenderName != "bob" {
		t.Errorf("SenderName = %q, want fallback to Acct", got.SenderName)
	}
}

func TestHandleNotification_WithAttachments(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "mention",
		Account: mastodon.Account{ID: "3", DisplayName: "Carol"},
		Status: &mastodon.Status{
			ID:      "s3",
			Content: "<p>pics</p>",
			MediaAttachments: []mastodon.Attachment{
				{ID: "img1", Type: "image", URL: "https://cdn/img1.jpg", Description: "a cat"},
				{ID: "vid1", Type: "video", URL: "https://cdn/vid1.mp4"},
			},
			CreatedAt: time.Now(),
		},
	}
	mc.handleNotification(n, rc)
	got := mr.last()
	if len(got.Attachments) != 2 {
		t.Fatalf("attachments = %d", len(got.Attachments))
	}
	if got.Attachments[0].Mime != "image/jpeg" {
		t.Errorf("mime[0] = %q", got.Attachments[0].Mime)
	}
	// URL uses hashed id; resolve round-trip via FileURL.
	prefix := "http://mastd:9004/files/"
	if !strings.HasPrefix(got.Attachments[0].URL, prefix) {
		t.Errorf("url[0] = %q", got.Attachments[0].URL)
	}
	if !strings.Contains(got.Content, "[image: a cat]") {
		t.Errorf("content missing image desc: %q", got.Content)
	}
	id := strings.TrimPrefix(got.Attachments[0].URL, prefix)
	if u, ok := mc.FileURL(id); !ok || u != "https://cdn/img1.jpg" {
		t.Errorf("FileURL = %q, ok=%v", u, ok)
	}
}

func TestHandleNotification_Favourite(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "favourite",
		Account: mastodon.Account{ID: "9", DisplayName: "Dan"},
		Status:  &mastodon.Status{ID: "s9"},
	}
	mc.handleNotification(n, rc)
	got := mr.last()
	if got.Verb != "like" {
		t.Errorf("verb = %q", got.Verb)
	}
	if got.ID != "fav-s9-9" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.Content != "❤️" {
		t.Errorf("Content = %q", got.Content)
	}
}

func TestHandleNotification_Reblog(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "reblog",
		Account: mastodon.Account{ID: "5", DisplayName: "Eve"},
		Status:  &mastodon.Status{ID: "orig-1"},
	}
	mc.handleNotification(n, rc)
	got := mr.last()
	if got.Verb != "repost" {
		t.Errorf("verb = %q", got.Verb)
	}
	if got.ID != "reblog-orig-1-5" {
		t.Errorf("ID = %q", got.ID)
	}
}

func TestHandleNotification_Follow(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	n := &mastodon.Notification{
		Type:    "follow",
		Account: mastodon.Account{ID: "6", DisplayName: "Frank"},
	}
	mc.handleNotification(n, rc)
	got := mr.last()
	if got.Verb != "follow" {
		t.Errorf("verb = %q", got.Verb)
	}
	if !strings.Contains(got.Content, "Frank") || !strings.Contains(got.Content, "followed") {
		t.Errorf("content = %q", got.Content)
	}
}

func TestHandleNotification_UnknownType_Skipped(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()

	mc.handleNotification(&mastodon.Notification{
		Type:    "poll",
		Account: mastodon.Account{ID: "1"},
	}, rc)
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Errorf("unknown type should not dispatch, got %d msgs", len(mr.msgs))
	}
}

func TestHandleNotification_MentionWithoutStatus_Skipped(t *testing.T) {
	mc := testMastoClient(t)
	rc, mr := testRouter(t)
	defer mr.close()
	mc.handleNotification(&mastodon.Notification{
		Type: "mention", Account: mastodon.Account{ID: "1"},
	}, rc)
	mr.mu.Lock()
	defer mr.mu.Unlock()
	if len(mr.msgs) != 0 {
		t.Errorf("mention without status should skip, got %d", len(mr.msgs))
	}
}

func TestSend_PostsToMastodon(t *testing.T) {
	var captured struct {
		sync.Mutex
		status      string
		inReplyToID string
		auth        string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/statuses" {
			r.ParseForm()
			captured.Lock()
			captured.status = r.Form.Get("status")
			captured.inReplyToID = r.Form.Get("in_reply_to_id")
			captured.auth = r.Header.Get("Authorization")
			captured.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"posted-1","content":"hi"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	mc := &mastoClient{
		cfg: config{InstanceURL: srv.URL, AccessToken: "tok"},
		client: mastodon.NewClient(&mastodon.Config{
			Server: srv.URL, AccessToken: "tok",
		}),
	}
	_, err := mc.Send(chanlib.SendRequest{ChatJID: "mastodon:1", Content: "hello world"})
	if err != nil {
		t.Fatal(err)
	}
	captured.Lock()
	defer captured.Unlock()
	if captured.status != "hello world" {
		t.Errorf("status = %q", captured.status)
	}
	if captured.auth != "Bearer tok" {
		t.Errorf("auth = %q", captured.auth)
	}
}

func TestSend_WithReply(t *testing.T) {
	var captured struct {
		sync.Mutex
		inReplyToID string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/statuses" {
			r.ParseForm()
			captured.Lock()
			captured.inReplyToID = r.Form.Get("in_reply_to_id")
			captured.Unlock()
			w.Write([]byte(`{"id":"p"}`))
			return
		}
		w.WriteHeader(404)
	}))
	defer srv.Close()

	mc := &mastoClient{
		cfg: config{InstanceURL: srv.URL},
		client: mastodon.NewClient(&mastodon.Config{
			Server: srv.URL, AccessToken: "tok",
		}),
	}
	_, err := mc.Send(chanlib.SendRequest{
		ChatJID: "mastodon:1", Content: "reply",
		ReplyTo: "parent-42",
	})
	if err != nil {
		t.Fatal(err)
	}
	captured.Lock()
	defer captured.Unlock()
	if captured.inReplyToID != "parent-42" {
		t.Errorf("in_reply_to_id = %q", captured.inReplyToID)
	}
}

func TestSend_UpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()
	mc := &mastoClient{
		cfg: config{InstanceURL: srv.URL},
		client: mastodon.NewClient(&mastodon.Config{
			Server: srv.URL, AccessToken: "tok",
		}),
	}
	_, err := mc.Send(chanlib.SendRequest{ChatJID: "mastodon:1", Content: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestTyping_Noop(t *testing.T) {
	mc := &mastoClient{}
	mc.Typing("mastodon:1", true) // no panic
}

func TestExtractAttachments_Multi(t *testing.T) {
	mc := &mastoClient{
		cfg:   config{ListenURL: "http://mastd:9004"},
		files: chanlib.NewURLCache(10),
	}
	s := &mastodon.Status{
		MediaAttachments: []mastodon.Attachment{
			{ID: "a1", Type: "image", URL: "https://cdn/a1.jpg"},
			{ID: "a2", Type: "video", URL: "https://cdn/a2.mp4"},
			{ID: "a3", Type: "audio", URL: "https://cdn/a3.mp3"},
			{ID: "a4", Type: "unknown", URL: "https://cdn/a4.bin"},
		},
	}
	atts := mc.extractAttachments(s)
	if len(atts) != 4 {
		t.Fatalf("atts = %d", len(atts))
	}
	if atts[0].Filename != "a1.jpg" {
		t.Errorf("filename[0] = %q", atts[0].Filename)
	}
	if atts[3].Mime != "application/octet-stream" {
		t.Errorf("mime[3] = %q", atts[3].Mime)
	}
}

func TestExtractAttachments_NoListenURL(t *testing.T) {
	mc := &mastoClient{cfg: config{ListenURL: ""}, files: chanlib.NewURLCache(10)}
	s := &mastodon.Status{MediaAttachments: []mastodon.Attachment{
		{ID: "x", Type: "image", URL: "https://cdn/x.jpg"},
	}}
	atts := mc.extractAttachments(s)
	if len(atts) != 1 || atts[0].URL != "" {
		t.Errorf("URL should be empty when ListenURL unset: %+v", atts)
	}
}

func TestEnvCacheSize_Default(t *testing.T) {
	t.Setenv("MASTODON_FILE_CACHE_SIZE", "")
	if n := envCacheSize(); n != 1000 {
		t.Errorf("default = %d", n)
	}
}

func TestEnvCacheSize_Custom(t *testing.T) {
	t.Setenv("MASTODON_FILE_CACHE_SIZE", "42")
	if n := envCacheSize(); n != 42 {
		t.Errorf("custom = %d", n)
	}
}

func TestEnvCacheSize_InvalidFallsBack(t *testing.T) {
	t.Setenv("MASTODON_FILE_CACHE_SIZE", "notanumber")
	if n := envCacheSize(); n != 1000 {
		t.Errorf("invalid should fall back, got %d", n)
	}
}

func TestFetchHistory_Notifications(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/notifications" {
			http.Error(w, "wrong path", 404)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Two mentions: one from account 42 (target), one from 99 (filtered out).
		w.Write([]byte(`[
			{"id":"n1","type":"mention","created_at":"2023-11-14T22:13:20Z","account":{"id":"42","display_name":"Alice","acct":"alice"},"status":{"id":"s-newer","content":"<p>newer</p>","created_at":"2023-11-14T22:13:20Z"}},
			{"id":"n2","type":"mention","created_at":"2023-11-14T22:13:10Z","account":{"id":"99","display_name":"Zed","acct":"zed"},"status":{"id":"s-other","content":"other","created_at":"2023-11-14T22:13:10Z"}},
			{"id":"n3","type":"mention","created_at":"2023-11-14T22:13:00Z","account":{"id":"42","display_name":"Alice","acct":"alice"},"status":{"id":"s-older","content":"<p>older</p>","created_at":"2023-11-14T22:13:00Z"}}
		]`))
	}))
	defer srv.Close()

	mc := &mastoClient{
		cfg: config{InstanceURL: srv.URL, ListenURL: "http://mastd:9004"},
		client: mastodon.NewClient(&mastodon.Config{
			Server: srv.URL, AccessToken: "tok",
		}),
		http:  srv.Client(),
		files: chanlib.NewURLCache(10),
	}

	resp, err := mc.FetchHistory(chanlib.HistoryRequest{ChatJID: "mastodon:account/42", Limit: 50})
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if resp.Source != "platform" {
		t.Errorf("Source=%q", resp.Source)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages=%d", len(resp.Messages))
	}
	// Oldest-first.
	if resp.Messages[0].ID != "s-older" || resp.Messages[1].ID != "s-newer" {
		t.Errorf("order wrong: %q %q", resp.Messages[0].ID, resp.Messages[1].ID)
	}
	if resp.Messages[0].ChatJID != "mastodon:account/42" {
		t.Errorf("ChatJID=%q", resp.Messages[0].ChatJID)
	}
	if resp.Messages[0].Content != "older" {
		t.Errorf("Content=%q (should be HTML-stripped)", resp.Messages[0].Content)
	}
}

func TestFetchHistory_UnsupportedJID(t *testing.T) {
	mc := &mastoClient{}
	resp, err := mc.FetchHistory(chanlib.HistoryRequest{ChatJID: "telegram:123"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Source != "unsupported" || len(resp.Messages) != 0 {
		t.Errorf("want unsupported empty, got %+v", resp)
	}
}

func TestMimeExt_Unknown(t *testing.T) {
	if mimeExt("application/octet-stream") != "" {
		t.Error("unknown mime should return empty string")
	}
}
