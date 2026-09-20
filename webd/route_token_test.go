package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// TestRouteToken_ResolvedInProcess pins the daemon boundary spec 5/W now
// describes: webd is FS-mounted on routd.db and resolves a URL token by DB
// lookup, never over HTTP. The router client here points at a closed port, so
// ANY HTTP hop to routd fails — the token surface must still serve. The spec
// used to claim the opposite (a POST /v1/route_tokens/resolve round trip),
// and that endpoint had zero callers; this test is why it can stay deleted.
func TestRouteToken_ResolvedInProcess(t *testing.T) {
	st := testutils.NewRoutdStore(t)
	seedGroup(t, st, "acme", "Acme")
	tok := seedChatToken(t, st, "acme")

	// Port 1 is closed: every routd HTTP call from this server errors out.
	rc := chanlib.NewRouterClient("http://127.0.0.1:1")
	rc.SetToken("test-token")
	s := newServer(config{assistantName: "assistant"}, st, newHub(), rc, nil, nil)
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/chat/" + tok + "/config")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d body=%s — token resolution left the process", resp.StatusCode, body)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["folder"] != "acme" {
		t.Fatalf("folder = %v, want acme", out["folder"])
	}
}

// TestRouteTokenStream_BoundToRecord is the containment check for
// /chat/stream: the token ROW decides both the auth folder and the
// subscription key. The client's legacy ?group= param is inert — before the
// fix it CHOSE the subscription key, so a caller could aim the subscription
// and a suffixed chat was unreachable (BUGS L30).
func TestRouteTokenStream_BoundToRecord(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "acme", "Acme")
	seedGroup(t, st, "victim", "Victim")
	tok := seedChatToken(t, st, "acme")
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	get := func(group string, hdr map[string]string) (*http.Response, context.CancelFunc) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		url := srv.URL + "/chat/stream?token=" + tok + "&topic=tt"
		if group != "" {
			url += "&group=" + group
		}
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			cancel()
			t.Fatalf("do(group=%s): %v", group, err)
		}
		return resp, cancel
	}

	// A lying ?group= neither redirects the key nor widens auth: the stream
	// opens on the TOKEN's folder and receives frames published on the token's
	// chat JID.
	resp, cancel := get("victim", nil)
	defer cancel()
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 — the record, not ?group=, authorizes", resp.StatusCode)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish("web:acme", "tt", "message", `{"role":"assistant","id":"b1"}`)
	}()
	r := bufio.NewReader(resp.Body)
	got := false
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"id":"b1"`) {
			got = true
			break
		}
	}
	if !got {
		t.Error("frame on the token's chat JID never arrived — subscription not bound to the record")
	}

	// ?group= must not WIDEN reach either. mallory holds an acme token and
	// claims victim; the stream she gets is acme's, and no frame published on
	// victim's chat JID ever reaches it.
	//
	// This half used to assert 403 for a caller whose grants covered only the
	// claimed group. That gate consulted grants because proxyd resolved the
	// token and webd compared its stamp; with resolution here (specs/6/22 step
	// 1) grants no longer enter the decision, so the containment is asserted
	// directly on the frames instead of on a status code.
	resp2, cancel2 := get("victim", signUserHeaders(map[string]string{
		"X-User-Sub":    "user:mallory",
		"X-User-Groups": `["victim"]`,
	}))
	defer cancel2()
	defer resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 — the token authorizes, ?group= is inert", resp2.StatusCode)
	}
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish("web:victim", "tt", "message", `{"role":"assistant","id":"leak"}`)
		s.hub.publish("web:acme", "tt", "message", `{"role":"assistant","id":"b2"}`)
	}()
	r2 := bufio.NewReader(resp2.Body)
	deadline = time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		line, err := r2.ReadString('\n')
		if err != nil {
			break
		}
		if strings.Contains(line, `"id":"leak"`) {
			t.Fatal("frame from the CLAIMED group arrived — ?group= aimed the subscription")
		}
		if strings.Contains(line, `"id":"b2"`) {
			break
		}
	}
}

// Publish/subscribe agreement for a SUFFIXED chat token (jid_suffix mint):
// chat JID "web:main/widget" differs from "web:"+target_folder ("web:main").
// The POST wait= subscriber and routd's /send callback (which carries the
// chat JID verbatim) must land on the same hub key — before the fix the POST
// subscribed on the target folder and never saw its own reply (BUGS L30).
func TestChatTokenPost_WaitSuffixedToken(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "web:main/widget", OwnerFolder: "main", TargetFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	assertWaitSeesReply(t, s, mr, srv.URL+"/chat/"+raw, "web:main/widget")
}

// Same agreement for a HOOK token used on the /chat surface (kind is
// metadata, spec 5/W): chat JID "hook:main/github" has no "web:" prefix, so
// every prefix-stripping derivation is a no-op on one end and a mangle on the
// other (BUGS L30). Verbatim-JID keying makes both ends meet.
func TestChatTokenPost_WaitHookToken(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "hook:main/github", OwnerFolder: "main", TargetFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	assertWaitSeesReply(t, s, mr, srv.URL+"/chat/"+raw, "hook:main/github")
}

// assertWaitSeesReply drives the end-to-end hub agreement: POST ?wait= holds
// the subscription while a goroutine plays routd's /send callback with the
// chat JID exactly as it appears on the inbound wire. The wait response must
// contain the assistant frame.
// waitBudget is the ?wait= this helper asks for and the deadline its replier
// honours. One value, so the two halves of the exchange cannot disagree.
const waitBudget = 5 * time.Second

func assertWaitSeesReply(t *testing.T, s *server, mr *mockRouter, chatURL, wantJID string) {
	t.Helper()
	go func() {
		// Wait for the inbound to reach the mock router, then reply the way
		// routd does: /send with the chat JID it holds (verbatim).
		//
		// ONE budget, the wait's. A tighter deadline here gives up while the
		// waiter is still waiting, and the failure then reads as "the hub keys
		// disagree" rather than "the inbound was slow" — which is what it said on
		// a loaded host once the inbound had a network store to cross.
		deadline := time.Now().Add(waitBudget)
		for time.Now().Before(deadline) {
			if sent := mr.sent(); len(sent) == 1 {
				if sent[0].ChatJID != wantJID {
					t.Errorf("inbound chat JID = %q, want %q", sent[0].ChatJID, wantJID)
				}
				payload, _ := json.Marshal(map[string]string{
					"chat_jid": sent[0].ChatJID, "content": "pong", "reply_to": sent[0].ID,
				})
				req := httptest.NewRequest("POST", "/send", bytes.NewReader(payload))
				w := httptest.NewRecorder()
				s.handleSend(w, req)
				if w.Code != 200 {
					t.Errorf("/send status = %d: %s", w.Code, w.Body.String())
				}
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("inbound never reached the router")
	}()

	payload, _ := json.Marshal(map[string]string{"content": "ping", "topic": "tw"})
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("%s?wait=%d", chatURL, int(waitBudget.Seconds())), bytes.NewReader(payload))
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
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	a, _ := out["assistant"].(map[string]any)
	if a == nil || a["content"] != "pong" {
		t.Fatalf("wait= missed the assistant frame (hub keys disagree): %v", out)
	}
}

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

// hookSender — the source label is the JID segment after the RECORDED delivery
// folder (spec 5/W), so a suffixed JID reads exactly instead of by heuristic:
// hook:atlas/linear/comments at folder atlas is source "linear", not "comments"
// (BUGS L4). A token minted before the column records no folder; those keep the
// last-segment answer they were minted under.
func TestHookSender(t *testing.T) {
	cases := []struct {
		name string
		row  store.RouteToken
		want string
	}{
		{"recorded folder, no suffix", store.RouteToken{JID: "hook:atlas/telegram", TargetFolder: "atlas"}, "telegram"},
		{"recorded folder, suffix", store.RouteToken{JID: "hook:atlas/linear/comments", TargetFolder: "atlas"}, "linear"},
		{"recorded nested folder", store.RouteToken{JID: "hook:atlas/eng/slack", TargetFolder: "atlas/eng"}, "slack"},
		{"recorded nested folder, suffix", store.RouteToken{JID: "hook:atlas/eng/slack/alerts", TargetFolder: "atlas/eng"}, "slack"},
		{"pre-column, no suffix", store.RouteToken{JID: "hook:atlas/telegram"}, "telegram"},
		{"pre-column, nested", store.RouteToken{JID: "hook:atlas/eng/slack"}, "slack"},
		{"no source segment", store.RouteToken{JID: "hook:bare", TargetFolder: "bare"}, ""},
		{"web token", store.RouteToken{JID: "web:atlas", TargetFolder: "atlas"}, ""},
		{"empty", store.RouteToken{}, ""},
	}
	for _, c := range cases {
		if got := hookSender(c.row); got != c.want {
			t.Errorf("%s: hookSender(%+v) = %q, want %q", c.name, c.row, got, c.want)
		}
	}
}

// parseChatBody — form and JSON paths; empty content is rejected.
func TestParseChatBody(t *testing.T) {
	body := strings.NewReader("content=hello&topic=t1")
	r := httptest.NewRequest("POST", "/chat/tok", body)
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	content, topic, err := parseChatBody(r)
	if err != nil || content != "hello" || topic != "t1" {
		t.Errorf("form: content=%q topic=%q err=%v", content, topic, err)
	}

	payload, _ := json.Marshal(map[string]string{"content": "hi json", "topic": "t2"})
	r2 := httptest.NewRequest("POST", "/chat/tok", bytes.NewReader(payload))
	r2.Header.Set("Content-Type", "application/json")
	content, topic, err = parseChatBody(r2)
	if err != nil || content != "hi json" || topic != "t2" {
		t.Errorf("json: content=%q topic=%q err=%v", content, topic, err)
	}

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

// TestE2EHookTokenPost — release-gate E2E via the webd route-token surface
// (make test-e2e): mint a hook: token, POST through it, verify 204 + the
// message lands at the router.
func TestE2EHookTokenPost(t *testing.T) {
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

// Link context (spec 5/W § link context): a chat POST via a context-bearing
// token snapshots the token's context onto the inbound wire message; a
// context-less token on the same JID carries none (no regression).
func TestHandleChatTokenPost_LinkContext(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	withCtx, plain := store.GenRouteToken(), store.GenRouteToken()
	if err := st.InsertRouteToken(withCtx, store.RouteToken{
		JID: "web:main", OwnerFolder: "main",
		Context: "bug reports from the acme site; triage, don't chat",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	if err := st.InsertRouteToken(plain, store.RouteToken{
		JID: "web:main", OwnerFolder: "main",
	}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	post := func(tok string) {
		t.Helper()
		body := strings.NewReader("content=hello&topic=tt")
		req, _ := http.NewRequest("POST", srv.URL+"/chat/"+tok, body)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	}
	post(withCtx)
	post(plain)

	sent := mr.sent()
	if len(sent) != 2 {
		t.Fatalf("router got %d messages, want 2", len(sent))
	}
	if sent[0].LinkContext != "bug reports from the acme site; triage, don't chat" {
		t.Errorf("context token: LinkContext = %q", sent[0].LinkContext)
	}
	if sent[1].LinkContext != "" {
		t.Errorf("context-less token: LinkContext = %q, want empty", sent[1].LinkContext)
	}
}

// Link context on the hook surface: POST /hook/<token> carries the token's
// context on the inbound wire message too.
func TestHandleHookTokenPost_LinkContext(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	raw := "hooktok-ctx"
	if err := st.InsertRouteToken(raw, store.RouteToken{
		JID: "hook:main/github", OwnerFolder: "main",
		Context: "github push events; summarize, no replies",
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
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	sent := mr.sent()
	if len(sent) != 1 || sent[0].LinkContext != "github push events; summarize, no replies" {
		t.Fatalf("router messages = %+v", sent)
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

// A token outlives its TARGET folder. route_tokens.owner_folder carries
// ON DELETE CASCADE, so deleting the folder that MINTED a token revokes it —
// but target_folder has no foreign key and may differ from the owner (routd
// migration 0035 says so outright). Delete that target and the token survives,
// routd's resolve finds no group, and the payload falls through to a route miss
// that parks an onbod admission row for a JID nobody can redeem.
//
// It is refused at ingress instead: a definite non-2xx to the caller and
// nothing sent to routd (BUGS L5). The 404 matches this surface's no-leak rule
// — the reason goes to the operator log, not to the caller.
func TestRouteToken_DeletedTargetFolderRefusedAtIngress(t *testing.T) {
	s, mr, st := newTestServer(t)
	seedGroup(t, st, "keeper", "Keeper")
	seedGroup(t, st, "doomed", "Doomed")

	// keeper OWNS both tokens; both DELIVER to doomed.
	hookTok := "hooktokdeleted"
	if err := st.InsertRouteToken(hookTok, store.RouteToken{
		JID: "hook:doomed/github", OwnerFolder: "keeper", TargetFolder: "doomed",
	}); err != nil {
		t.Fatalf("InsertRouteToken hook: %v", err)
	}
	chatTok := "chattokdeleted"
	if err := st.InsertRouteToken(chatTok, store.RouteToken{
		JID: "web:doomed", OwnerFolder: "keeper", TargetFolder: "doomed",
	}); err != nil {
		t.Fatalf("InsertRouteToken chat: %v", err)
	}

	// The operator deletes the delivery target. No cascade reaches these rows.
	if _, _, err := st.DeleteGroup("doomed"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}
	if _, ok := st.LookupRouteToken(hookTok); !ok {
		t.Fatal("token vanished with the folder: this test no longer covers the gap it was written for")
	}

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("POST", srv.URL+"/hook/"+hookTok, strings.NewReader("event payload"))
	req.Header.Set("Content-Type", "text/plain")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("hook status = %d, want 404: a token for a deleted folder must not be accepted", resp.StatusCode)
	}

	cfg, err := http.Get(srv.URL + "/chat/" + chatTok + "/config")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Body.Close()
	if cfg.StatusCode != http.StatusNotFound {
		t.Errorf("chat config status = %d, want 404", cfg.StatusCode)
	}

	if sent := mr.sent(); len(sent) != 0 {
		t.Errorf("%d message(s) reached routd; the payload must stop at ingress: %+v", len(sent), sent)
	}
}
