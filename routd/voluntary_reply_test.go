package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// A Stop check nudges an owed reply once; silence and explicit delivery remain
// distinct, and a reaction counts as delivery just like a text reply.
func TestCheckTurnDeliveryDecision(t *testing.T) {
	for _, tt := range []struct {
		name, verb, sentence       string
		addressed, owed, delivered bool
	}{
		{name: "addressed", addressed: true, owed: true, sentence: "You were addressed"},
		{name: "status", verb: "status", owed: true, sentence: "You posted a status"},
		{name: "ambient"},
		{name: "reply", addressed: true, verb: "reply", owed: true, delivered: true},
		{name: "reaction", addressed: true, verb: "like", owed: true, delivered: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			srv := NewServer(db, nil, nil, nil, 0, "")
			if _, err := db.PutTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnAddressed("t1", tt.addressed); err != nil {
				t.Fatal(err)
			}
			if tt.verb != "" {
				if err := db.PutMessage(core.Message{ID: "out", ChatJID: "tg:1", TurnID: "t1", Verb: tt.verb,
					BotMsg: true, Timestamp: time.Now(), Content: "working"}); err != nil {
					t.Fatal(err)
				}
			}
			first, err := srv.mcpCheckTurn("t1")
			if err != nil {
				t.Fatal(err)
			}
			if first.Owed != tt.owed || first.Delivered != tt.delivered || (first.Nudge != "") != (tt.sentence != "") || !strings.Contains(first.Nudge, tt.sentence) {
				t.Fatalf("first check=%+v", first)
			}
			second, err := srv.mcpCheckTurn("t1")
			if err != nil || second.Nudge != "" {
				t.Fatalf("second check=%+v err=%v", second, err)
			}
			tc, _ := db.GetTurnContext("t1")
			want := 0
			if tt.sentence != "" {
				want = 1
			}
			if tc.Nudged != want {
				t.Fatalf("nudged=%d want=%d", tc.Nudged, want)
			}
		})
	}
}

// The hidden turn check cannot name another turn through a bound MCP socket.
func TestCheckTurnSocketBinding(t *testing.T) {
	srv := &Server{}
	fns := srv.buildGatedFns(turnMCP{turnID: "bound"})
	if _, err := fns.CheckTurn("demo", "other"); err == nil {
		t.Fatal("foreign turn accepted")
	}
}
