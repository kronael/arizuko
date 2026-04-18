package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
)

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
	for _, g := range resp.Data {
		if !g.Active {
			t.Errorf("group %q not active by default", g.Folder)
		}
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
