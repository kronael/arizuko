//go:build browser

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

const browserSession = "arizuko-webd-progress"

func runBrowser(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("agent-browser", append([]string{"--session", browserSession, "--json"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("agent-browser %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func evalPage(t *testing.T, script string) string {
	t.Helper()
	var answer struct {
		Success bool `json:"success"`
		Data    struct {
			Result any `json:"result"`
		} `json:"data"`
	}
	out := runBrowser(t, "eval", script)
	if err := json.Unmarshal([]byte(out), &answer); err != nil || !answer.Success {
		t.Fatalf("eval %s: %v\n%s", script, err, out)
	}
	return fmt.Sprint(answer.Data.Result)
}

func waitPage(t *testing.T, script, want string) {
	t.Helper()
	got := ""
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		if got = evalPage(t, script); got == want {
			return
		}
	}
	t.Fatalf("%s = %q, want %q", script, got, want)
}

const (
	streamOpen     = `String(!!cur&&!!es&&es.readyState===1)`
	streamReopened = `String(!!es&&es!==window.previousStream&&es.readyState===1)`
	statusLines    = `Array.from(document.querySelectorAll('[data-status="1"]')).map(function(n){return n.textContent}).join('|')`
)

type progressLine struct {
	t      *testing.T
	s      *server
	st     *store.Store
	turnID string
	topic  string
}

func openProgressLine(t *testing.T, s *server, st *store.Store, turnID, topic string) progressLine {
	t.Helper()
	if _, err := st.OpenTurnContext(turnID, "main", topic, "web:main", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	return progressLine{t, s, st, turnID, topic}
}

func (p progressLine) row() core.Message {
	id := "status-" + p.turnID
	if row, ok := p.st.MessageByID("web:main", id); ok {
		return row
	}
	return core.Message{ID: id, ChatJID: "web:main", Topic: p.topic, TurnID: p.turnID, Sender: "main", RoutedTo: "main", FromMe: true, BotMsg: true, Verb: verbStatus, Timestamp: time.Now()}
}

func (p progressLine) deliver(phase, text string, handle http.HandlerFunc, body map[string]string) {
	p.t.Helper()
	if err := p.st.QueueStatus(p.t.Context(), p.row(), text, phase); err != nil {
		p.t.Fatal(err)
	}
	row := p.row()
	body["chat_jid"] = row.ChatJID
	if row.PlatformID != "" {
		body["target_id"] = row.PlatformID
	}
	raw, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	handle(w, httptest.NewRequest("POST", "/", strings.NewReader(string(raw))))
	var answer struct {
		ID string `json:"id"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &answer) != nil {
		p.t.Fatalf("%s: %d %s", phase, w.Code, w.Body.String())
	}
	if err := p.st.MarkDelivered(p.t.Context(), row, answer.ID); err != nil {
		p.t.Fatal(err)
	}
}

func (p progressLine) show(text string) {
	p.t.Helper()
	if p.row().PlatformID == "" {
		p.deliver("progress", text, p.s.handleSend, map[string]string{"content": text, "idempotency_key": p.row().ID})
		return
	}
	p.deliver("progress", text, p.s.handleEdit, map[string]string{"content": text})
}

func (p progressLine) end() {
	p.t.Helper()
	if err := p.st.SetTurnState(p.turnID, store.TurnDone); err != nil {
		p.t.Fatal(err)
	}
	p.deliver("cleared", p.row().Content, p.s.handleDelete, map[string]string{})
}

// The built-in page shows one progress line per turn, replaces it on an edit,
// removes it on delete, and after a reconnect or a reload shows exactly the
// active snapshot the server holds.
func TestBrowserProgress(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	server := httptest.NewServer(s.handler())
	defer server.Close()
	runBrowser(t, "open", server.URL+"/chat/"+token+"/")
	defer runBrowser(t, "close")
	waitPage(t, streamOpen, "true")
	topic := evalPage(t, "cur.topic")

	dropStream := func() {
		t.Helper()
		evalPage(t, "window.previousStream=es,''")
		server.CloseClientConnections()
	}
	reload := func() {
		t.Helper()
		runBrowser(t, "reload")
		waitPage(t, streamOpen, "true")
	}

	first := openProgressLine(t, s, st, "turn-1", topic)
	first.show("⏳ ☐ read the report")
	waitPage(t, statusLines, "⏳ ☐ read the report")
	first.show("⏳ ☑ read the report")
	waitPage(t, statusLines, "⏳ ☑ read the report")
	first.end()
	waitPage(t, statusLines, "")

	second := openProgressLine(t, s, st, "turn-2", topic)
	second.show("⏳ ☐ write the answer")
	waitPage(t, statusLines, "⏳ ☐ write the answer")
	reload()
	waitPage(t, statusLines, "⏳ ☐ write the answer")

	dropStream()
	second.show("⏳ ☑ write the answer")
	waitPage(t, streamReopened, "true")
	waitPage(t, statusLines, "⏳ ☑ write the answer")

	dropStream()
	second.end()
	waitPage(t, streamReopened, "true")
	waitPage(t, statusLines, "")
	reload()
	waitPage(t, statusLines, "")

	third := openProgressLine(t, s, st, "turn-3", topic)
	third.show("⏳ ☐ deletion fails")
	waitPage(t, statusLines, "⏳ ☐ deletion fails")
	if err := st.SetTurnState("turn-3", store.TurnDone); err != nil {
		t.Fatal(err)
	}
	dropStream()
	waitPage(t, streamReopened, "true")
	waitPage(t, statusLines, "")
	reload()
	waitPage(t, statusLines, "")
}
