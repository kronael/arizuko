package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// putBotRow stores one assistant row the way routd does after a send: its own
// id, the id webd handed back as platform_id, and the turn and topic.
func putBotRow(t *testing.T, st *store.Store, chat, id, platformID, verb, topic, turn string) {
	t.Helper()
	if err := st.PutMessage(core.Message{
		ID: id, ChatJID: chat, Content: "⏳ first", Timestamp: time.Now(),
		Topic: topic, TurnID: turn, BotMsg: true, Verb: verb, PlatformID: platformID,
	}); err != nil {
		t.Fatalf("put %s: %v", id, err)
	}
}

func postEdit(s *server, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/edit", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, req)
	return w
}

func recvFrame(t *testing.T, ch <-chan string) (event string, p map[string]any) {
	t.Helper()
	select {
	case msg := <-ch:
		ev, data := splitSSEFrame(msg)
		if err := json.Unmarshal([]byte(data), &p); err != nil {
			t.Fatalf("frame payload %q: %v", data, err)
		}
		return ev, p
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no hub publish")
	}
	return "", nil
}

// An edit reaches the browser as a `message` frame with the id of the frame it
// already shows, so the browser replaces rather than appends.
func TestEdit_PublishesReplaceFrameWithSameID(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	putBotRow(t, st, "web:main", "out-1", "bot-1", "status", "tt", "turn1")

	ch, unsub := s.hub.subscribe("web:main", "tt")
	defer unsub()

	w := postEdit(s, `{"chat_jid":"web:main","target_id":"bot-1","content":"⏳ second"}`)
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	ev, p := recvFrame(t, ch)
	if ev != "message" {
		t.Errorf("event = %q, want message", ev)
	}
	if p["id"] != "bot-1" {
		t.Errorf("frame id = %v, want bot-1 (the frame being replaced)", p["id"])
	}
	if p["content"] != "⏳ second" {
		t.Errorf("frame content = %v", p["content"])
	}
	if p["turn_id"] != "turn1" || p["kind"] != "status" || p["role"] != "assistant" {
		t.Errorf("frame = %v, want turn_id turn1, kind status, role assistant", p)
	}
}

// The verb decides the kind: an edit of an ordinary reply is a `message` frame,
// whatever its text starts with.
func TestEdit_KindFollowsTheRowVerb(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	putBotRow(t, st, "web:main", "out-2", "bot-2", "message", "tt", "turn2")

	ch, unsub := s.hub.subscribe("web:main", "tt")
	defer unsub()

	if w := postEdit(s, `{"chat_jid":"web:main","target_id":"bot-2","content":"⏳ not progress"}`); w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if _, p := recvFrame(t, ch); p["kind"] != "message" {
		t.Errorf("kind = %v, want message", p["kind"])
	}
}

// The hub key is the chat JID from the wire, never the token's target folder
// (BUGS L30): the replace frame of a suffixed chat reaches the per-turn SSE
// stream keyed on that JID and does not reach the bare-folder key.
func TestEdit_SuffixedChatReachesTurnSSE(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	const jid = "web:main/widget"
	raw := store.GenRouteToken()
	if err := st.InsertRouteToken(raw, store.RouteToken{JID: jid, OwnerFolder: "main", TargetFolder: "main"}); err != nil {
		t.Fatalf("InsertRouteToken: %v", err)
	}
	if err := st.PutMessage(core.Message{
		ID: "turnS", ChatJID: jid, Content: "q", Timestamp: time.Now(), Topic: "tt", TurnID: "turnS",
	}); err != nil {
		t.Fatal(err)
	}
	putBotRow(t, st, jid, "out-s", "bot-s", "status", "tt", "turnS")

	folderKey, unsubF := s.hub.subscribe("web:main", "tt")
	defer unsubF()

	srv := httptest.NewServer(s.handler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/chat/"+raw+"/turnS/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do: %v", err)
	}
	defer resp.Body.Close()

	go func() {
		time.Sleep(50 * time.Millisecond)
		postEdit(s, `{"chat_jid":"`+jid+`","target_id":"bot-s","content":"⏳ replaced"}`)
	}()

	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the replace frame arrived: %v", err)
		}
		if strings.Contains(line, `"content":"⏳ replaced"`) && strings.Contains(line, `"id":"bot-s"`) {
			break
		}
	}
	select {
	case msg := <-folderKey:
		t.Errorf("replace frame reached the bare-folder key: %s", msg)
	default:
	}
}

// Fail loud: an edit that names nothing webd can find, a row another chat
// owns, or a row that is not an assistant message publishes nothing.
func TestEdit_Refusals(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	putBotRow(t, st, "web:other", "out-o", "bot-o", "status", "tt", "turnO")
	if err := st.PutMessage(core.Message{
		ID: "u-1", ChatJID: "web:main", Content: "hi", Timestamp: time.Now(), Topic: "tt", TurnID: "u-1",
	}); err != nil {
		t.Fatal(err)
	}

	ch, unsub := s.hub.subscribe("web:main", "tt")
	defer unsub()
	chO, unsubO := s.hub.subscribe("web:other", "tt")
	defer unsubO()

	cases := []struct {
		name, body string
		want       int
	}{
		{"unknown target", `{"chat_jid":"web:main","target_id":"nope","content":"x"}`, 404},
		{"target in another chat", `{"chat_jid":"web:main","target_id":"bot-o","content":"x"}`, 404},
		{"user message", `{"chat_jid":"web:main","target_id":"u-1","content":"x"}`, 403},
		{"no content", `{"chat_jid":"web:main","target_id":"bot-o"}`, 400},
		{"no target", `{"chat_jid":"web:main","content":"x"}`, 400},
		{"no chat", `{"target_id":"bot-o","content":"x"}`, 400},
		{"not json", `nope`, 400},
	}
	for _, c := range cases {
		if w := postEdit(s, c.body); w.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", c.name, w.Code, c.want, w.Body.String())
		}
	}
	for _, c := range []<-chan string{ch, chO} {
		select {
		case msg := <-c:
			t.Errorf("a refused edit published %s", msg)
		default:
		}
	}
}

// A row with Verb status and text without the glyph is a status frame; a row
// with the glyph and no verb is a message frame.
func TestMessageFrames_KindFromVerb(t *testing.T) {
	frames := messageFrames([]core.Message{
		{ID: "a", Content: "reading the file", Verb: "status"},
		{ID: "b", Content: "⏳ looks like progress", Verb: "message"},
		{ID: "c", Content: "⏳ looks like progress"},
	})
	want := []string{"status", "message", "message"}
	for i, f := range frames {
		if f.Kind != want[i] {
			t.Errorf("frame %s kind = %q, want %q", f.ID, f.Kind, want[i])
		}
	}
}

// webd renders no presence, so it advertises send_text and edit only. A
// capability with a no-op behind it is the drift CapImplReport exists to catch.
func TestRegistrationCaps(t *testing.T) {
	if len(webCaps) != 2 || !webCaps["send_text"] || !webCaps["edit"] {
		t.Errorf("webCaps = %v, want exactly send_text and edit", webCaps)
	}
	if webCaps["typing"] {
		t.Error("webCaps advertises typing; webd renders no presence")
	}
}
