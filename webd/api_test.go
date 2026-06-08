package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
)

// POST /v1/round_done — the channelSecret gate rejects an unauthenticated
// caller; a valid POST resolves turn_id→topic and publishes round_done to the
// folder/topic key the SSE subscriber listens on. This drives every SSE
// turn-close, so a regression hangs the chat widget.
func TestHandleRoundDone(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")

	// (a) channelSecret gate: a non-empty secret + no Authorization → 401.
	gated := chanlib.Auth("sekret", s.handleRoundDone)
	req := httptest.NewRequest("POST", "/v1/round_done",
		strings.NewReader(`{"turn_id":"t1","folder":"main","status":"ok"}`))
	w := httptest.NewRecorder()
	gated(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthed status = %d, want 401", w.Code)
	}

	// (b) valid call: seed the turn message so turn_id→topic resolves, then
	// assert publish lands on the matching folder/topic key.
	turnID := "turn-rd"
	if err := st.PutMessage(core.Message{
		ID: turnID, ChatJID: "web:main", Content: "q",
		Timestamp: time.Now(), Topic: "tt", TurnID: turnID,
	}); err != nil {
		t.Fatalf("PutMessage: %v", err)
	}
	ch, unsub := s.hub.subscribe("main", "tt")
	defer unsub()

	body := `{"turn_id":"` + turnID + `","folder":"main","status":"ok"}`
	req = httptest.NewRequest("POST", "/v1/round_done", strings.NewReader(body))
	w = httptest.NewRecorder()
	gated.ServeHTTP(w, withBearer(req, "sekret"))
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	select {
	case frame := <-ch:
		if !strings.Contains(frame, `"turn_id":"`+turnID+`"`) ||
			!strings.Contains(frame, "round_done") {
			t.Errorf("round_done frame wrong: %s", frame)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no round_done publish on the turn's folder/topic key")
	}
}

func withBearer(r *http.Request, tok string) *http.Request {
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

// GET /api/groups returns groups the user has ACL for, via s.groupList().
func TestAPIGroups_ListsAll(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	seedGroup(t, st, "other", "Other")

	req := httptest.NewRequest("GET", "/api/groups", nil)
	w := httptest.NewRecorder()
	s.handleAPIGroups(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Data []groupOut `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 2 {
		t.Fatalf("groups = %d, want 2: %+v", len(resp.Data), resp.Data)
	}
}

// GET /api/groups/<folder>/topics returns topic summaries.
func TestAPITopics(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")

	now := time.Now()
	_ = st.PutMessage(core.Message{
		ID: "m1", ChatJID: "web:main", Content: "hello",
		Timestamp: now, Topic: "ta",
	})

	req := httptest.NewRequest("GET", "/api/groups/main/topics", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleAPITopics(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"ta"`) {
		t.Errorf("want topic id ta in body: %s", w.Body.String())
	}
}

// GET /api/groups/<folder>/messages returns the topic message list with
// role mapping (bot => assistant).
func TestAPIMessages(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")

	now := time.Now()
	_ = st.PutMessage(core.Message{
		ID: "u1", ChatJID: "web:main", Content: "ping",
		Timestamp: now.Add(-time.Second), Topic: "t1", Name: "Alice",
	})
	_ = st.PutMessage(core.Message{
		ID: "b1", ChatJID: "web:main", Content: "pong",
		Timestamp: now, Topic: "t1", BotMsg: true,
	})

	req := httptest.NewRequest("GET", "/api/groups/main/messages?topic=t1", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleAPIMessages(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, `"role":"assistant"`) {
		t.Errorf("bot role should map to assistant: %s", body)
	}
	if !strings.Contains(body, `"role":"user"`) {
		t.Errorf("user role missing: %s", body)
	}
}

// Invalid before= parameter falls through to default (now), returning the
// regular list — the code path swallows the parse error.
func TestAPIMessages_InvalidBefore(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	_ = st.PutMessage(core.Message{
		ID: "x1", ChatJID: "web:main", Content: "x",
		Timestamp: time.Now(), Topic: "t1",
	})

	req := httptest.NewRequest("GET",
		"/api/groups/main/messages?topic=t1&before=not-a-date", nil)
	req.SetPathValue("folder", "main")
	w := httptest.NewRecorder()
	s.handleAPIMessages(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

// POST /api/groups/<folder>/typing returns 202 Accepted (no-op).
func TestAPITyping(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/api/groups/main/typing", nil)
	w := httptest.NewRecorder()
	s.handleAPITyping(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", w.Code)
	}
}

// POST /send (channel callback from gated) writes bot message, publishes
// on the hub, and returns the new id.
func TestHandleSend(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")

	// Subscribe to hub before send so we can assert publish happened.
	ch, unsub := s.hub.subscribe("main", "")
	defer unsub()

	body := `{"chat_jid":"web:main","content":"bot reply"}`
	req := httptest.NewRequest("POST", "/send", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handleSend(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.OK || resp.ID == "" {
		t.Fatalf("bad resp: %s", w.Body.String())
	}

	// Bot message persisted?
	msgs, err := st.MessagesByTopic("main", "", time.Now().Add(time.Second), 10)
	if err != nil || len(msgs) != 1 || !msgs[0].BotMsg {
		t.Fatalf("store missing bot msg: %+v err=%v", msgs, err)
	}

	// Hub received publish?
	select {
	case frame := <-ch:
		if !strings.Contains(frame, `"role":"assistant"`) {
			t.Errorf("hub frame wrong: %s", frame)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no hub publish")
	}
}

// /send rejects missing chat_jid or content.
func TestHandleSend_BadBody(t *testing.T) {
	s, _, _ := newTestServer(t)
	cases := []string{
		`{}`,
		`{"chat_jid":"web:main"}`,
		`{"content":"hi"}`,
		`not json`,
	}
	for _, body := range cases {
		req := httptest.NewRequest("POST", "/send", strings.NewReader(body))
		w := httptest.NewRecorder()
		s.handleSend(w, req)
		if w.Code != 400 {
			t.Errorf("body=%q status = %d, want 400", body, w.Code)
		}
	}
}

// /typing is a liveness no-op — returns {ok:true}.
func TestHandleTyping(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("POST", "/typing", nil)
	w := httptest.NewRecorder()
	s.handleTyping(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Errorf("body = %s", w.Body.String())
	}
}

// /health reports the adapter metadata.
func TestHandleHealth(t *testing.T) {
	s, _, _ := newTestServer(t)
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.handleHealth(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var got map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if got["status"] != "ok" || got["name"] != "web" {
		t.Errorf("health payload wrong: %+v", got)
	}
}
