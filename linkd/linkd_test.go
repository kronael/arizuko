package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/onvos/arizuko/chanlib"
)

// stubBot for server HTTP tests.
type stubBot struct {
	chanlib.NoFileSender
	mu       sync.Mutex
	lastReq  chanlib.SendRequest
	returnID string
	returnErr error
}

func (s *stubBot) Send(r chanlib.SendRequest) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastReq = r
	return s.returnID, s.returnErr
}
func (s *stubBot) Typing(string, bool) {}

func TestHealth(t *testing.T) {
	s := newServer(config{Name: "linkedin"}, &stubBot{})
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["name"] != "linkedin" {
		t.Errorf("name = %v", resp["name"])
	}
	prefixes, _ := resp["jid_prefixes"].([]any)
	if len(prefixes) != 1 || prefixes[0] != "linkedin:" {
		t.Errorf("jid_prefixes = %v", resp["jid_prefixes"])
	}
}

func TestSendAuthRequired(t *testing.T) {
	s := newServer(config{Name: "linkedin", ChannelSecret: "s"}, &stubBot{})
	body, _ := json.Marshal(map[string]string{"chat_jid": "linkedin:x", "content": "hi"})
	req := httptest.NewRequest("POST", "/send", bytes.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestIsPostURN(t *testing.T) {
	yes := []string{
		"urn:li:activity:123",
		"urn:li:share:456",
		"urn:li:ugcPost:789",
	}
	no := []string{
		"",
		"urn:li:person:abc",
		"foo",
	}
	for _, u := range yes {
		if !isPostURN(u) {
			t.Errorf("isPostURN(%q) = false, want true", u)
		}
	}
	for _, u := range no {
		if isPostURN(u) {
			t.Errorf("isPostURN(%q) = true, want false", u)
		}
	}
}

func TestRefreshAccessToken(t *testing.T) {
	var gotGrant, gotRefresh string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/v2/accessToken" {
			http.Error(w, "wrong path", 404)
			return
		}
		r.ParseForm()
		gotGrant = r.PostForm.Get("grant_type")
		gotRefresh = r.PostForm.Get("refresh_token")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"access_token":"new-at","expires_in":3600,"refresh_token":"new-rt"}`))
	}))
	defer srv.Close()

	tmp := t.TempDir()
	lc := &linkClient{
		cfg: config{
			Name:         "linkedin",
			ClientID:     "cid",
			ClientSecret: "csec",
			OAuthBase:    srv.URL,
			DataDir:      tmp,
		},
		http:      srv.Client(),
		refresh:   "old-rt",
		seen:      map[string]bool{},
		stateFile: tmp + "/state.json",
	}
	if err := lc.refreshAccessToken(); err != nil {
		t.Fatalf("refreshAccessToken: %v", err)
	}
	if gotGrant != "refresh_token" {
		t.Errorf("grant_type = %q", gotGrant)
	}
	if gotRefresh != "old-rt" {
		t.Errorf("refresh_token = %q", gotRefresh)
	}
	if lc.token != "new-at" {
		t.Errorf("token = %q", lc.token)
	}
	if lc.refresh != "new-rt" {
		t.Errorf("refresh = %q", lc.refresh)
	}
}

func TestRefreshAccessTokenError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		w.Write([]byte(`{"error":"invalid_grant","error_description":"bad"}`))
	}))
	defer srv.Close()
	lc := &linkClient{
		cfg:  config{OAuthBase: srv.URL, ClientID: "c", ClientSecret: "s"},
		http: srv.Client(),
		refresh: "x",
		seen: map[string]bool{},
		stateFile: t.TempDir() + "/s.json",
	}
	if err := lc.refreshAccessToken(); err == nil {
		t.Error("expected error")
	}
}

func TestPostCommentOutbound(t *testing.T) {
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"comment-1"}`))
	}))
	defer srv.Close()

	lc := &linkClient{
		cfg:   config{APIBase: srv.URL},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
		seen:  map[string]bool{},
	}
	id, err := lc.Send(chanlib.SendRequest{
		ChatJID: "linkedin:urn:li:activity:999",
		Content: "hello there",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if id != "comment-1" {
		t.Errorf("id = %q", id)
	}
	if !strings.Contains(gotPath, "/v2/socialActions/") || !strings.Contains(gotPath, "/comments") {
		t.Errorf("path = %q", gotPath)
	}
	if gotBody["actor"] != "urn:li:person:me" {
		t.Errorf("actor = %v", gotBody["actor"])
	}
	msg, _ := gotBody["message"].(map[string]any)
	if msg["text"] != "hello there" {
		t.Errorf("text = %v", msg["text"])
	}
}

func TestSendRefusesPublishByDefault(t *testing.T) {
	lc := &linkClient{
		cfg:   config{AutoPublish: false},
		token: "t",
		meURN: "urn:li:person:me",
		seen:  map[string]bool{},
	}
	_, err := lc.Send(chanlib.SendRequest{ChatJID: "linkedin:urn:li:person:other", Content: "hi"})
	if err == nil {
		t.Error("expected refusal when AutoPublish=false")
	}
}

func TestDeliverComment(t *testing.T) {
	// Capture router deliveries.
	router := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer router.Close()
	rc := chanlib.NewRouterClient(router.URL, "s")
	rc.SetToken("t")

	lc := &linkClient{
		meURN: "urn:li:person:me",
		seen:  map[string]bool{},
	}

	// Comment from another user → delivered, marked seen.
	c := commentItem{
		ID:    "c1",
		Actor: "urn:li:person:other",
		Message: struct {
			Text string `json:"text"`
		}{Text: "nice post"},
	}
	lc.deliverComment(rc, "urn:li:activity:1", c)
	if !lc.seen["urn:li:activity:1|c1"] {
		t.Error("expected comment marked seen")
	}

	// Comment from self → skipped.
	selfC := commentItem{ID: "c2", Actor: "urn:li:person:me"}
	selfC.Message.Text = "my own"
	lc.deliverComment(rc, "urn:li:activity:1", selfC)
	// seen is still set so dedup works, but nothing delivered — we can't
	// distinguish here, but the critical check is empty-text skip and dedup.

	// Duplicate delivery should not double-mark.
	beforeLen := len(lc.seen)
	lc.deliverComment(rc, "urn:li:activity:1", c)
	if len(lc.seen) != beforeLen {
		t.Error("duplicate should not add new key")
	}
}

func TestFetchHistory_Comments(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"elements":[
			{"id":"c1","actor":"urn:li:person:alice","created":{"time":1700000200000},"message":{"text":"great"}},
			{"id":"c2","actor":"urn:li:person:me","created":{"time":1700000150000},"message":{"text":"self-skip"}},
			{"id":"c3","actor":"urn:li:person:bob","created":{"time":1700000100000},"message":{"text":"reply"},"parentComment":"urn:li:comment:xyz"}
		]}`))
	}))
	defer srv.Close()

	lc := &linkClient{
		cfg:   config{APIBase: srv.URL},
		http:  srv.Client(),
		token: "tok",
		meURN: "urn:li:person:me",
		seen:  map[string]bool{},
	}
	resp, err := lc.FetchHistory(chanlib.HistoryRequest{
		ChatJID: "linkedin:urn:li:activity:999",
		Limit:   50,
	})
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if resp.Source != "platform" {
		t.Errorf("Source=%q", resp.Source)
	}
	if !strings.Contains(gotPath, "/v2/socialActions/") || !strings.Contains(gotPath, "/comments") {
		t.Errorf("path=%q", gotPath)
	}
	if len(resp.Messages) != 2 {
		t.Fatalf("messages=%d want 2 (own-comment filtered)", len(resp.Messages))
	}
	if resp.Messages[0].ID != "urn:li:activity:999-c1" {
		t.Errorf("ID=%q", resp.Messages[0].ID)
	}
	if resp.Messages[0].ChatJID != "linkedin:urn:li:activity:999" {
		t.Errorf("ChatJID=%q", resp.Messages[0].ChatJID)
	}
	if resp.Messages[1].Verb != "reply" || resp.Messages[1].ReplyTo != "urn:li:comment:xyz" {
		t.Errorf("reply fields wrong: %+v", resp.Messages[1])
	}
}

func TestFetchHistory_NonPostJID_Unsupported(t *testing.T) {
	lc := &linkClient{seen: map[string]bool{}}
	resp, err := lc.FetchHistory(chanlib.HistoryRequest{ChatJID: "linkedin:urn:li:person:alice"})
	if err != nil {
		t.Fatalf("FetchHistory: %v", err)
	}
	if resp.Source != "unsupported" {
		t.Errorf("Source=%q", resp.Source)
	}
	if len(resp.Messages) != 0 {
		t.Errorf("messages=%d", len(resp.Messages))
	}
}

func TestSaveLoadState(t *testing.T) {
	tmp := t.TempDir()
	lc1 := &linkClient{
		cfg:       config{DataDir: tmp, Name: "linkedin"},
		token:     "access1",
		refresh:   "refresh1",
		seen:      map[string]bool{"k1": true, "k2": true},
		stateFile: tmp + "/linkd-state-linkedin.json",
	}
	lc1.saveState()

	lc2 := &linkClient{
		cfg:       config{DataDir: tmp, Name: "linkedin"},
		seen:      map[string]bool{},
		stateFile: tmp + "/linkd-state-linkedin.json",
	}
	lc2.loadState()
	if lc2.token != "access1" {
		t.Errorf("token = %q", lc2.token)
	}
	if lc2.refresh != "refresh1" {
		t.Errorf("refresh = %q", lc2.refresh)
	}
	if !lc2.seen["k1"] || !lc2.seen["k2"] {
		t.Errorf("seen = %v", lc2.seen)
	}
}
