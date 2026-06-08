package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// groupfolder.JidFolder — all JID prefix shapes (webd's route-token consumer).
func TestJIDFolder(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"web:atlas", "atlas"},
		{"web:atlas/content", "atlas/content"},
		{"web:", ""},
		{"hook:atlas/telegram", "atlas"},
		{"hook:atlas/eng/telegram", "atlas/eng"},
		{"hook:bare", "bare"},
		{"other:x", ""},
	}
	for _, c := range cases {
		got := groupfolder.JidFolder(c.jid)
		if got != c.want {
			t.Errorf("JidFolder(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}

// senderFromJID — hook: JID extracts last path segment as source.
func TestSenderFromJID(t *testing.T) {
	cases := []struct {
		jid  string
		want string
	}{
		{"hook:atlas/telegram", "telegram"},
		{"hook:atlas/eng/slack", "slack"},
		{"hook:bare", ""},
		{"web:atlas", ""},
		{"", ""},
	}
	for _, c := range cases {
		got := senderFromJID(c.jid)
		if got != c.want {
			t.Errorf("senderFromJID(%q) = %q, want %q", c.jid, got, c.want)
		}
	}
}

// parseChatBody — form and JSON paths; empty content is rejected.
func TestParseChatBody(t *testing.T) {
	// form body
	body := strings.NewReader("content=hello&topic=t1")
	r := httptest.NewRequest("POST", "/chat/tok", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	content, topic, err := parseChatBody(r)
	if err != nil || content != "hello" || topic != "t1" {
		t.Errorf("form: content=%q topic=%q err=%v", content, topic, err)
	}

	// JSON body
	payload, _ := json.Marshal(map[string]string{"content": "hi json", "topic": "t2"})
	r2 := httptest.NewRequest("POST", "/chat/tok", bytes.NewReader(payload))
	r2.Header.Set("Content-Type", "application/json")
	content, topic, err = parseChatBody(r2)
	if err != nil || content != "hi json" || topic != "t2" {
		t.Errorf("json: content=%q topic=%q err=%v", content, topic, err)
	}

	// empty content → error
	r3 := httptest.NewRequest("POST", "/chat/tok", strings.NewReader("content="))
	r3.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if _, _, err := parseChatBody(r3); err == nil {
		t.Error("empty content should error")
	}
}

// chatCORS sets headers and short-circuits OPTIONS.
func TestChatCORS(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(200)
	})
	h := chatCORS(next)

	// OPTIONS must short-circuit.
	r := httptest.NewRequest("OPTIONS", "/chat/tok", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if called {
		t.Error("OPTIONS should short-circuit handler")
	}
	if w.Code != http.StatusNoContent {
		t.Errorf("OPTIONS status = %d, want 204", w.Code)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS header missing")
	}

	// GET passes through.
	called = false
	r2 := httptest.NewRequest("GET", "/chat/tok/", nil)
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if !called {
		t.Error("GET should reach handler")
	}
}

// htmlEscape covers HTML injection vectors.
func TestHTMLEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{`<script>`, `&lt;script&gt;`},
		{`"foo"`, `&#34;foo&#34;`},
		{`'bar'`, `&#39;bar&#39;`},
		{`a&b`, `a&amp;b`},
		{`a\b`, `a&#92;b`},
		{`>gt<`, `&gt;gt&lt;`},
	}
	for _, c := range cases {
		got := htmlEscape(c.in)
		if got != c.want {
			t.Errorf("htmlEscape(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// GET /chat/<token>/ → 404 for unknown token; 200 for known token.
func TestHandleChatTokenRoot(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	// Unknown token.
	resp, err := http.Get(srv.URL + "/chat/unknowntok/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown token: status = %d, want 404", resp.StatusCode)
	}

	// Known token.
	resp, err = http.Get(srv.URL + "/chat/" + tok + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("known token: status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	if !strings.Contains(string(body), "dock") {
		t.Errorf("chat widget missing in response")
	}
}

// GET /chat/<token>/config returns JSON bootstrap.
func TestHandleChatTokenConfig(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "grp", "Grp")
	tok := seedChatToken(t, st, "grp")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/" + tok + "/config")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["token"] != tok {
		t.Errorf("token = %v, want %q", out["token"], tok)
	}
	if out["folder"] != "grp" {
		t.Errorf("folder = %v, want grp", out["folder"])
	}
	eps, _ := out["endpoints"].(map[string]any)
	if eps == nil || eps["post"] == "" {
		t.Error("endpoints.post missing")
	}
}

// GET /chat/<token>/<topic>/messages returns empty list for unknown topic.
func TestHandleChatTokenHistory_EmptyTopic(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/" + tok + "/no-such-topic/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out []any
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out) != 0 {
		t.Errorf("want empty list, got %v", out)
	}
}

// GET /chat/<token>/<topic>/messages with an overlong topic → 400.
func TestHandleChatTokenHistory_TopicTooLong(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	long := strings.Repeat("x", maxTopicLen+1)
	resp, err := http.Get(srv.URL + "/chat/" + tok + "/" + long + "/messages")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// POST /chat/<token> with Accept: text/html returns HTMX bubble.
func TestHandleChatTokenPost_HTML(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	body := strings.NewReader("content=hello+world&topic=tt")
	req, _ := http.NewRequest("POST", srv.URL+"/chat/"+tok, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), `class="msg user"`) {
		t.Errorf("HTMX bubble missing: %q", b)
	}
	if strings.Contains(string(b), "<script>") {
		t.Error("potential XSS in bubble")
	}
}

// POST /chat/<token> with JSON body returns JSON envelope.
func TestHandleChatTokenPost_JSON(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	payload, _ := json.Marshal(map[string]string{"content": "ping", "topic": "t-json"})
	req, _ := http.NewRequest("POST", srv.URL+"/chat/"+tok, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "pending" {
		t.Errorf("status = %v, want pending", out["status"])
	}
	if out["turn_id"] == "" {
		t.Error("turn_id missing")
	}
}

// POST /hook/<token> → 204 + message forwarded to router.
func TestHandleHookTokenPost(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	// Seed a hook: token directly via the store helper.
	raw := "hooktok1234"
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "hook:main/webhook", OwnerFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/hook/"+raw, strings.NewReader("event payload"))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("status = %d, want 204", resp.StatusCode)
	}
	// Router must have received the inbound message.
	sent := mr.sent()
	if len(sent) != 1 {
		t.Errorf("router got %d messages, want 1", len(sent))
	}
}

// POST /hook/<token> with unknown token → 404.
func TestHandleHookTokenPost_UnknownToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/hook/unknowntok", strings.NewReader("x"))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

// splitSSEFrame extracts event + data from a multi-line SSE frame.
func TestSplitSSEFrame(t *testing.T) {
	frame := "event: round_done\ndata: {\"turn_id\":\"t1\"}\n\n"
	ev, data := splitSSEFrame(frame)
	if ev != "round_done" {
		t.Errorf("event = %q, want round_done", ev)
	}
	if data != `{"turn_id":"t1"}` {
		t.Errorf("data = %q", data)
	}
}

// POST /chat/<token> with XSS content in HTMX response is escaped.
func TestHandleChatTokenPost_XSS(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	evil := "<script>alert(1)</script>"
	body := strings.NewReader("content=" + evil + "&topic=xss")
	req, _ := http.NewRequest("POST", srv.URL+"/chat/"+tok, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(b), "<script>") {
		t.Errorf("XSS not escaped in HTMX response: %q", b)
	}
}

// POST /chat/<token> with a signed user stamped by proxyd identifies as that user (not anon).
func TestHandleChatTokenPost_AuthenticatedSender(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	payload, _ := json.Marshal(map[string]string{"content": "authenticated msg", "topic": "t-auth"})
	req, _ := http.NewRequest("POST", srv.URL+"/chat/"+tok, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	// Stamp identity headers as proxyd would.
	for k, v := range signUserHeaders(map[string]string{
		"X-User-Sub":    "user:alice",
		"X-User-Name":   "Alice",
		"X-User-Groups": `["main"]`,
	}) {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	// Router should receive the authenticated sender sub.
	sent := mr.sent()
	if len(sent) != 1 {
		t.Fatalf("router got %d messages", len(sent))
	}
	if sent[0].Sender != "user:alice" {
		t.Errorf("sender = %q, want user:alice", sent[0].Sender)
	}
}

// GET /chat/<token>/<topic>/messages returns seeded messages oldest→newest.
func TestHandleChatTokenHistory_WithMessages(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	tok := seedChatToken(t, st, "main")

	now := time.Now()
	st.PutMessage(core.Message{
		ID: "m1", ChatJID: "web:main", Content: "first",
		Timestamp: now.Add(-2 * time.Second), Topic: "mytopic",
	})
	st.PutMessage(core.Message{
		ID: "m2", ChatJID: "web:main", Content: "second",
		Timestamp: now.Add(-time.Second), Topic: "mytopic", BotMsg: true,
	})

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/" + tok + "/mytopic/messages")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var msgs []map[string]any
	json.NewDecoder(resp.Body).Decode(&msgs)
	if len(msgs) != 2 {
		t.Fatalf("len(msgs) = %d, want 2", len(msgs))
	}
	// oldest first
	if msgs[0]["id"] != "m1" {
		t.Errorf("msgs[0].id = %v, want m1", msgs[0]["id"])
	}
	if msgs[1]["role"] != "assistant" {
		t.Errorf("msgs[1].role = %v, want assistant", msgs[1]["role"])
	}
}
