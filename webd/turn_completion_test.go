package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type completingReplayStore struct {
	objstore.Store
	server *server
	st     *store.Store
	fired  bool
}

func (s *completingReplayStore) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if !s.fired && strings.HasPrefix(q.Partition, "message/status/") {
		s.fired = true
		err := s.st.PutMessage(core.Message{ID: "late-reply", ChatJID: "web:main",
			Content: "answer", Timestamp: time.Now(), Topic: "tt", TurnID: "turn", BotMsg: true})
		if err != nil {
			return nil, err
		}
		s.server.hub.publish("web:main", "tt", "message", `{"id":"late-reply","turn_id":"turn","content":"answer"}`)
		if _, err := s.st.RecordTurnResult("main", "turn", "", "success", "", store.TurnUsage{}); err != nil {
			return nil, err
		}
		s.server.hub.publish("web:main", "tt", "round_done", `{"turn_id":"turn","status":"success"}`)
	}
	return s.Store.Query(ctx, q)
}

func TestTurnSSECompletionDuringReplay(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	err := st.PutMessage(core.Message{ID: "turn", ChatJID: "web:main", Content: "q",
		Timestamp: time.Now().Add(-time.Second), Topic: "tt", TurnID: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	wrapped := &completingReplayStore{Store: st.Obj(), server: s, st: st}
	s.stRoutd = store.NewOver(wrapped)
	w := httptest.NewRecorder()
	s.handler().ServeHTTP(w, httptest.NewRequest("GET", "/chat/"+token+"/turn/sse", nil))
	body := w.Body.String()
	reply, done := strings.Index(body, "late-reply"), strings.Index(body, "event: round_done")
	if !wrapped.fired || reply < 0 || done < reply {
		t.Fatalf("status=%d fired=%v body=%q", w.Code, wrapped.fired, body)
	}
}
