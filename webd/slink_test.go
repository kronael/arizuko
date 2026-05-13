package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// POST with Content-Type: application/json and {"content":"..."} body
// is accepted (regression — atlas/strengths submit form 2026-05-11).
func TestSlinkPost_JSONBody(t *testing.T) {
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader(`{"content":"hello-json","topic":"t-jsonbody"}`))
	req.Header.Set("Content-Type", "application/json")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if got := mr.sent(); len(got) != 1 || got[0].Content != "hello-json" {
		t.Errorf("router did not receive json-body message: %+v", got)
	}
}

// POST with Accept: text/html returns the legacy HTMX bubble fragment.
func TestSlinkPost_HTMLBubble(t *testing.T) {
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=hello&topic=t1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `class="msg user"`) {
		t.Errorf("body missing user bubble: %q", w.Body.String())
	}
	if got := mr.sent(); len(got) != 1 || got[0].Content != "hello" {
		t.Errorf("router did not receive message: %+v", got)
	}
}

// POST with no Accept header defaults to JSON (Z-slink-widget.md §JSON-default).
func TestSlinkPost_DefaultIsJSON(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=default&topic=t-default"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("default Content-Type should be JSON, got %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("default response must be JSON: %v — body=%s", err, w.Body.String())
	}
	if _, ok := got["turn_id"]; !ok {
		t.Errorf("missing turn_id in default JSON response: %+v", got)
	}
}

// POST with Accept: text/html opts into the legacy HTMX fragment.
func TestSlinkPost_AcceptHTMLFragment(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=frag&topic=t-frag"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Accept: text/html should yield HTML, got Content-Type=%q", ct)
	}
	if !strings.Contains(w.Body.String(), `class="msg user"`) {
		t.Errorf("body missing user bubble: %q", w.Body.String())
	}
}

// POST with Accept: text/event-stream holds the connection open and streams
// both the caller's own bubble and later hub publishes.
func TestSlinkPost_SSE_HeldOpen(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	body := strings.NewReader("content=hi&topic=t42")
	req, _ := http.NewRequestWithContext(ctx, "POST",
		srv.URL+"/slink/"+g.SlinkToken, body)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type = %q", ct)
	}

	// Publish a follow-up bot message on the same topic.
	go func() {
		time.Sleep(50 * time.Millisecond)
		s.hub.publish(g.Folder, "t42", "message", `{"role":"assistant","content":"pong"}`)
	}()

	var gotUser, gotAssistant bool
	r := bufio.NewReader(resp.Body)
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) && !(gotUser && gotAssistant) {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			break
		}
		if strings.Contains(line, `"role":"user"`) {
			gotUser = true
		}
		if strings.Contains(line, `"role":"assistant"`) {
			gotAssistant = true
		}
	}
	if !gotUser {
		t.Error("expected user bubble on SSE stream")
	}
	if !gotAssistant {
		t.Error("expected assistant message on SSE stream")
	}
}

// POST with Accept: application/json returns {user: {...}} and no SSE frame.
func TestSlinkPost_JSON(t *testing.T) {
	s, mr, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("POST", "/slink/"+g.SlinkToken,
		strings.NewReader("content=ping&topic=t-json"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()

	s.handleSlinkPost(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if ct := w.Result().Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type = %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v — body=%s", err, w.Body.String())
	}
	user, ok := got["user"].(map[string]any)
	if !ok {
		t.Fatalf("missing user: %+v", got)
	}
	if user["content"] != "ping" || user["role"] != "user" || user["topic"] != "t-json" {
		t.Errorf("user payload wrong: %+v", user)
	}
	if got["turn_id"] != user["id"] {
		t.Errorf("turn_id should equal user.id (=inbound msg_id), got: %v vs %v", got["turn_id"], user["id"])
	}
	if got["status"] != "pending" {
		t.Errorf("status should be pending on async-default response, got: %v", got["status"])
	}
	if _, has := got["assistant"]; has {
		t.Errorf("assistant unexpectedly present without ?wait: %+v", got)
	}
	if len(mr.sent()) != 1 {
		t.Errorf("router did not receive exactly one message: %+v", mr.sent())
	}
}

// TurnSnapshot returns frames from store.TurnFrames matching the given id;
// asserts ordering, status transition, cursor paging.
func TestTurnSnapshot_PagingAndStatus(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	// Seed one inbound (= turn_id), then a few outbound frames stamped with it.
	turnID := "msg-inbound-1"
	chatJID := "web:" + g.Folder
	t0 := time.Now()
	mk := func(id, sender, content string, bot bool, tid string, off time.Duration) core.Message {
		return core.Message{
			ID: id, ChatJID: chatJID, Sender: sender, Content: content,
			Timestamp: t0.Add(off), BotMsg: bot, TurnID: tid,
		}
	}
	if err := st.PutMessage(mk(turnID, "user", "ping", false, "", 0)); err != nil {
		t.Fatalf("seed inbound: %v", err)
	}
	if err := st.PutMessage(mk("out-1", "main", "first reply", true, turnID, 1*time.Millisecond)); err != nil {
		t.Fatalf("seed out1: %v", err)
	}
	if err := st.PutMessage(mk("out-2", "main", "second reply", true, turnID, 2*time.Millisecond)); err != nil {
		t.Fatalf("seed out2: %v", err)
	}

	// Snapshot, full.
	req := httptest.NewRequest("GET", "/slink/"+g.SlinkToken+"/"+turnID, nil)
	req.SetPathValue("token", g.SlinkToken)
	req.SetPathValue("id", turnID)
	w := httptest.NewRecorder()
	s.handleTurnSnapshot(w, req)
	if w.Code != 200 {
		t.Fatalf("snapshot status %d: %s", w.Code, w.Body.String())
	}
	var snap map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap["status"] != "pending" {
		t.Errorf("status before submit_turn should be pending, got: %v", snap["status"])
	}
	frames, _ := snap["frames"].([]any)
	if len(frames) != 2 {
		t.Fatalf("frames count = %d, want 2: %+v", len(frames), snap)
	}
	if snap["last_frame_id"] != "out-2" {
		t.Errorf("last_frame_id = %v, want out-2", snap["last_frame_id"])
	}

	// Cursor paging — only frames after out-1 should come back.
	req = httptest.NewRequest("GET", "/slink/"+g.SlinkToken+"/"+turnID+"?after=out-1", nil)
	req.SetPathValue("token", g.SlinkToken)
	req.SetPathValue("id", turnID)
	w = httptest.NewRecorder()
	s.handleTurnSnapshot(w, req)
	json.Unmarshal(w.Body.Bytes(), &snap)
	frames, _ = snap["frames"].([]any)
	if len(frames) != 1 {
		t.Fatalf("paged frames = %d, want 1", len(frames))
	}

	// Status flips to "success" once turn_results lands.
	if _, err := st.RecordTurnResult(g.Folder, turnID, "session-1", "success"); err != nil {
		t.Fatalf("record turn: %v", err)
	}
	req = httptest.NewRequest("GET", "/slink/"+g.SlinkToken+"/"+turnID+"/status", nil)
	req.SetPathValue("token", g.SlinkToken)
	req.SetPathValue("id", turnID)
	w = httptest.NewRecorder()
	s.handleTurnStatus(w, req)
	if w.Code != 200 {
		t.Fatalf("status endpoint = %d", w.Code)
	}
	var stat map[string]any
	json.Unmarshal(w.Body.Bytes(), &stat)
	if stat["status"] != "success" {
		t.Errorf("status after RecordTurnResult = %v, want success", stat["status"])
	}
	if int(stat["frames_count"].(float64)) != 2 {
		t.Errorf("frames_count = %v, want 2", stat["frames_count"])
	}
}

// postSlinkJSON POSTs form-encoded body to /slink/<token><query> with
// Accept: application/json and returns the decoded response + elapsed.
func postSlinkJSON(t *testing.T, url, body string) (map[string]any, time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return got, time.Since(start)
}

// POST ?wait=<sec> with Accept: application/json blocks until an assistant
// reply is published on the hub, then returns {user, assistant}.
func TestSlinkPost_JSON_WaitReturnsAssistant(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	go func() {
		time.Sleep(80 * time.Millisecond)
		s.hub.publish(g.Folder, "t-wait", "message",
			`{"role":"assistant","content":"pong","id":"bot-1"}`)
	}()

	got, _ := postSlinkJSON(t, srv.URL+"/slink/"+g.SlinkToken+"?wait=2",
		"content=ping&topic=t-wait")

	user, _ := got["user"].(map[string]any)
	if user == nil || user["content"] != "ping" {
		t.Fatalf("missing/bad user: %+v", got)
	}
	assistant, _ := got["assistant"].(map[string]any)
	if assistant == nil {
		t.Fatalf("missing assistant (wait mode should return it): %+v", got)
	}
	if assistant["content"] != "pong" || assistant["role"] != "assistant" {
		t.Errorf("assistant payload wrong: %+v", assistant)
	}
}

// POST ?wait=<sec> returns only {user} when no assistant replies in time.
func TestSlinkPost_JSON_WaitTimesOut(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	got, elapsed := postSlinkJSON(t, srv.URL+"/slink/"+g.SlinkToken+"?wait=1",
		"content=silent&topic=t-timeout")

	if elapsed < 900*time.Millisecond {
		t.Errorf("returned too fast (%s) — should have waited ~1s", elapsed)
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("returned too slow (%s)", elapsed)
	}
	if _, ok := got["user"]; !ok {
		t.Errorf("missing user: %+v", got)
	}
	if _, ok := got["assistant"]; ok {
		t.Errorf("unexpected assistant on timeout: %+v", got)
	}
}

// GET /slink/<token> 301-redirects to /slink/<token>/chat.
func TestSlinkGet_RootRedirects(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("GET", "/slink/"+g.SlinkToken, nil)
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()
	s.handleSlinkRoot(w, req)

	if w.Code != http.StatusMovedPermanently {
		t.Fatalf("status = %d, want 301", w.Code)
	}
	if loc := w.Result().Header.Get("Location"); loc != "/slink/"+g.SlinkToken+"/chat" {
		t.Errorf("Location = %q, want /slink/<token>/chat", loc)
	}
}

// GET /slink/<bad-token> → 404 even on the redirecting root route.
func TestSlinkGet_RootBadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/slink/nope", nil)
	req.SetPathValue("token", "nope")
	w := httptest.NewRecorder()
	s.handleSlinkRoot(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

// GET /slink/<token>/chat serves the minimal built-in chat page.
func TestSlinkChat_ServesPage(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("GET", "/slink/"+g.SlinkToken+"/chat", nil)
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()
	s.handleSlinkChat(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, g.Name) {
		t.Error("page missing group name")
	}
	if !strings.Contains(body, g.SlinkToken) {
		t.Error("page missing slink token in JS")
	}
	if !strings.Contains(body, "ant link") {
		t.Error("page missing ant link label")
	}
	// The inline JS must send Accept: text/html so the POST default (JSON)
	// doesn't break the built-in chat page.
	if !strings.Contains(body, `'Accept':'text/html'`) {
		t.Error("chat page inline JS missing Accept: text/html on its HTMX POST")
	}
}

// GET /slink/<token>/config returns the JSON bootstrap envelope.
func TestSlinkConfig_ReturnsBootstrap(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	req := httptest.NewRequest("GET", "/slink/"+g.SlinkToken+"/config", nil)
	req.SetPathValue("token", g.SlinkToken)
	w := httptest.NewRecorder()
	s.handleSlinkConfig(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["token"] != g.SlinkToken {
		t.Errorf("token = %v, want %s", got["token"], g.SlinkToken)
	}
	if got["folder"] != g.Folder {
		t.Errorf("folder = %v, want %s", got["folder"], g.Folder)
	}
	if got["name"] != g.Name {
		t.Errorf("name = %v, want %s", got["name"], g.Name)
	}
	ep, ok := got["endpoints"].(map[string]any)
	if !ok {
		t.Fatalf("missing endpoints: %+v", got)
	}
	if ep["post"] != "/slink/"+g.SlinkToken {
		t.Errorf("endpoints.post = %v", ep["post"])
	}
	if _, ok := ep["stream"]; !ok {
		t.Errorf("endpoints.stream missing")
	}
	if _, ok := ep["status"]; !ok {
		t.Errorf("endpoints.status missing")
	}
	if got["sdk"] != "/assets/arizuko-client.js" {
		t.Errorf("sdk = %v, want /assets/arizuko-client.js", got["sdk"])
	}
}

// GET /slink/badtoken/config → 404.
func TestSlinkConfig_BadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/slink/badtoken/config", nil)
	req.SetPathValue("token", "badtoken")
	w := httptest.NewRecorder()
	s.handleSlinkConfig(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

// OPTIONS /slink/<token> preflight returns 204 with CORS headers.
func TestSlinkCORS_PreflightOptions(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	req, _ := http.NewRequest("OPTIONS", srv.URL+"/slink/"+g.SlinkToken, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Methods"); !strings.Contains(got, "POST") {
		t.Errorf("ACAM = %q, want includes POST", got)
	}
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "Content-Type") {
		t.Errorf("ACAH = %q, want includes Content-Type", got)
	}
}

// Every /slink/* response — here the config GET — has Access-Control-Allow-Origin.
func TestSlinkCORS_GETHasOrigin(t *testing.T) {
	s, _, st := newTestServer(t)
	g := seedGroup(t, st, "main", "Main")

	srv := httptest.NewServer(s.handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/slink/" + g.SlinkToken + "/config")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("ACAO = %q, want *", got)
	}
}

// Bad token → 404.
func TestSlinkPost_BadToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/slink/nope",
		strings.NewReader("content=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("token", "nope")
	w := httptest.NewRecorder()
	s.handleSlinkPost(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}
