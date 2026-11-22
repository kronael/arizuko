package main

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestTurnSSEReplaysEveryPage(t *testing.T) {
	s, _, st := newTestServer(t)
	seedGroup(t, st, "main", "Main")
	token := seedChatToken(t, st, "main")
	now := time.Now().Add(-time.Hour)
	err := st.PutMessage(core.Message{ID: "turn", ChatJID: "web:main", Content: "q",
		Timestamp: now, Topic: "tt", TurnID: "turn"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 201 {
		err := st.PutMessage(core.Message{ID: fmt.Sprintf("reply-%03d", i), ChatJID: "web:main",
			Content: fmt.Sprint(i), Timestamp: now.Add(time.Duration(i+1) * time.Millisecond),
			Topic: "tt", TurnID: "turn", BotMsg: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.RecordTurnResult("main", "turn", "", "success", "", store.TurnUsage{}); err != nil {
		t.Fatal(err)
	}
	for _, after := range []string{"", "reply-199"} {
		req := httptest.NewRequest("GET", "/chat/"+token+"/turn/sse", nil)
		req.Header.Set("Last-Event-Id", after)
		w := httptest.NewRecorder()
		s.handler().ServeHTTP(w, req)
		want := 201
		if after != "" {
			want = 1
		}
		body := w.Body.String()
		if n := strings.Count(body, "event: message\n"); n != want {
			t.Fatalf("after=%q HTTP=%d frames=%d want=%d", after, w.Code, n, want)
		}
		last, done := strings.Index(body, "reply-200"), strings.Index(body, "event: round_done")
		if last < 0 || done < last {
			t.Fatalf("completion precedes last reply: %q", body)
		}
	}
}
