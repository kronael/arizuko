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
)

func TestProgress_FirstSendUsesStoredIdentity(t *testing.T) {
	s, _, st := newTestServer(t)
	row := core.Message{ID: "status-id", ChatJID: "web:main", Topic: "topic", TurnID: "turn", BotMsg: true, Verb: "status", Content: "working", Timestamp: time.Now()}
	if err := st.PutOutbound(context.Background(), row, core.MessageStatusPending); err != nil {
		t.Fatal(err)
	}
	ch, unsub := s.hub.subscribe(row.ChatJID, row.Topic)
	defer unsub()
	w := httptest.NewRecorder()
	s.handleSend(w, httptest.NewRequest("POST", "/send", strings.NewReader(`{"chat_jid":"web:main","content":"working","idempotency_key":"status-id"}`)))
	if !strings.Contains(w.Body.String(), `"id":"status-id"`) {
		t.Fatalf("response %s", w.Body.String())
	}
	select {
	case msg := <-ch:
		if !strings.Contains(msg, `"kind":"status"`) || !strings.Contains(msg, `"turn_id":"turn"`) || !strings.Contains(msg, `"id":"status-id"`) {
			t.Fatalf("frame %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing status frame")
	}
	foreign, cancel := s.hub.subscribe("web:other", "")
	defer cancel()
	w = httptest.NewRecorder()
	s.handleSend(w, httptest.NewRequest("POST", "/send", strings.NewReader(`{"chat_jid":"web:other","content":"ordinary","idempotency_key":"status-id"}`)))
	select {
	case msg := <-foreign:
		if strings.Contains(msg, `"kind":"status"`) || strings.Contains(msg, `"turn_id":"turn"`) {
			t.Fatalf("foreign identity leaked: %s", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("missing ordinary frame")
	}
}

func TestProgress_TurnSnapshotKeepsNormalCursor(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	base := time.Now().Add(-time.Minute)
	rows := []core.Message{
		{ID: "turn", ChatJID: "web:main", Topic: "tt", Content: "question", Timestamp: base},
		{ID: "progress", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Verb: "status", Content: "working", Timestamp: base.Add(time.Second)},
		{ID: "cursor", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Content: "cursor", Timestamp: base.Add(2 * time.Second)},
		{ID: "answer", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Content: "answer", Timestamp: base.Add(3 * time.Second)},
	}
	for _, row := range rows {
		if err := st.PutMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.OpenTurnContext("turn", "main", "tt", "web:main", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	progress, ok := st.MessageByID("web:main", "progress")
	if !ok {
		t.Fatal("missing progress")
	}
	if err := st.SetMessageContent(progress, "edited"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/?after=cursor", nil)
	req.SetPathValue("token", token)
	req.SetPathValue("id", "turn")
	w := httptest.NewRecorder()
	s.handleTurnSnapshot(w, req)
	var response struct {
		Frames []frame `json:"frames"`
		Last   string  `json:"last_frame_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Last != "answer" || len(response.Frames) != 2 || response.Frames[0].Kind != "status" || response.Frames[0].Content != "edited" {
		t.Fatalf("snapshot: %s %v", w.Body.String(), err)
	}
	req.URL.RawQuery = "after=answer"
	w = httptest.NewRecorder()
	s.handleTurnSnapshot(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Last != "" || len(response.Frames) != 1 || response.Frames[0].Content != "edited" {
		t.Fatalf("status only: %s %v", w.Body.String(), err)
	}
	req.URL.RawQuery = "after=cursor"
	if err := st.SetTurnState("turn", "done"); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	s.handleTurnSnapshot(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Frames) != 1 || response.Frames[0].ID != "answer" {
		t.Fatalf("ended snapshot: %s %v", w.Body.String(), err)
	}
}

func TestProgress_BufferedDeletePrecedesSnapshot(t *testing.T) {
	ch := make(chan string, 2)
	ch <- "event: delete\ndata: {\"id\":\"a\",\"turn_id\":\"t\"}\n\n"
	ch <- "event: round_done\ndata: {\"turn_id\":\"u\"}\n\n"
	queued, active := bufferedProgress(ch, []frame{{ID: "a", TurnID: "t"}, {ID: "b", TurnID: "u"}})
	if len(queued) != 2 || len(active) != 0 {
		t.Fatalf("queued %v active %v", queued, active)
	}
	if !strings.Contains(progressEvent(active), `"frames":[]`) {
		t.Fatal("empty snapshot missing")
	}
}

func TestProgress_TurnSSEReplaysStatusWithoutCursor(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	base := time.Now().Add(-time.Minute)
	for _, row := range []core.Message{
		{ID: "turn", ChatJID: "web:main", Topic: "tt", Content: "question", Timestamp: base},
		{ID: "progress", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Verb: "status", Content: "working", Timestamp: base.Add(time.Second)},
	} {
		if err := st.PutMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.OpenTurnContext("turn", "main", "tt", "web:main", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/chat/"+token+"/turn/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var data strings.Builder
	for !strings.Contains(data.String(), "event: progress") {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		data.WriteString(line)
	}
	if !strings.Contains(data.String(), "event: status\n") || strings.Contains(data.String(), "id: progress") {
		t.Fatalf("replay: %s", data.String())
	}
	w := httptest.NewRecorder()
	if !s.writeTurnEvent(w, nil, "web:main", "turn", "event: delete\ndata: {\"turn_id\":\"other\",\"id\":\"progress\"}\n\n") || w.Body.Len() != 0 {
		t.Fatalf("foreign delete: %s", w.Body.String())
	}
}

func TestProgress_RecheckUsesLatestAndDropsEndedEvents(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	if _, err := st.OpenTurnContext("turn", "main", "tt", "web:main", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	row := core.Message{ID: "progress", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Verb: "status", Content: "latest", Timestamp: time.Now()}
	if err := st.PutMessage(row); err != nil {
		t.Fatal(err)
	}
	frames, err := s.currentProgress("web:main", "tt", "", []frame{{ID: "progress", TurnID: "turn", Content: "old"}})
	if err != nil || len(frames) != 1 || frames[0].Content != "latest" {
		t.Fatalf("recheck: %+v %v", frames, err)
	}
	msg := "event: message\ndata: {\"id\":\"progress\",\"turn_id\":\"turn\",\"kind\":\"status\",\"content\":\"edit\"}\n\n"
	if !s.activeProgressEvent("web:main", "tt", msg) {
		t.Fatal("active edit dropped")
	}
	if err := st.SetTurnState("turn", "done"); err != nil {
		t.Fatal(err)
	}
	if s.activeProgressEvent("web:main", "tt", msg) {
		t.Fatal("ended edit restored status")
	}
}

func TestProgress_TopicHistoryAndReconnect(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	base := time.Now().Add(-time.Minute)
	for _, row := range []core.Message{
		{ID: "progress", ChatJID: "web:main", Topic: "tt", TurnID: "turn", BotMsg: true, Verb: "status", Content: "working", Timestamp: base},
		{ID: "cursor", ChatJID: "web:main", Topic: "tt", Content: "later", Timestamp: base.Add(time.Second)},
	} {
		if err := st.PutMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.OpenTurnContext("turn", "main", "tt", "web:main", "u", "", nil); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s.handler())
	defer server.Close()
	resp, err := http.Get(server.URL + "/chat/" + token + "/tt/messages")
	if err != nil {
		t.Fatal(err)
	}
	var history []struct {
		ID     string `json:"id"`
		Kind   string `json:"kind"`
		TurnID string `json:"turn_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&history); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(history) != 2 || history[0].ID != "progress" || history[0].Kind != "status" || history[0].TurnID != "turn" {
		t.Fatalf("history: %+v", history)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/chat/stream?token="+token+"&topic=tt", nil)
	req.Header.Set("Last-Event-ID", "cursor")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	var data strings.Builder
	for !strings.Contains(data.String(), `"frames":`) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		data.WriteString(line)
	}
	if !strings.Contains(data.String(), `"id":"progress"`) {
		t.Fatalf("reconnect: %s", data.String())
	}
}
