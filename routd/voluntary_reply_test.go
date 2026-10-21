package routd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// A Stop check nudges an owed reply once; silence and explicit delivery remain
// distinct. Social delivery is checked through its real audit path below.
func TestCheckTurnDeliveryDecision(t *testing.T) {
	for _, tt := range []struct {
		name, verb, sentence       string
		addressed, owed, delivered bool
	}{
		{name: "addressed", addressed: true, owed: true, sentence: "You were addressed"},
		{name: "status", verb: "status", owed: true, sentence: "You posted a status"},
		{name: "ambient"},
		{name: "reply", addressed: true, verb: "reply", owed: true, delivered: true},
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

// Concurrent Stop readers must spend one shared nudge, rather than each
// receiving its own continuation while their store reads are in flight.
func TestCheckTurnConcurrentReadersShareOneNudge(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := NewServer(db, nil, nil, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnAddressed("t1", true); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	type result struct {
		nudge string
		err   error
	}
	results := make(chan result, 8)
	for range cap(results) {
		go func() {
			<-start
			check, err := srv.mcpCheckTurn("t1")
			results <- result{nudge: check.Nudge, err: err}
		}()
	}
	close(start)
	nudges := 0
	for range cap(results) {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if got.nudge != "" {
			nudges++
		}
	}
	tc, _ := db.GetTurnContext("t1")
	if nudges != 1 || tc.Nudged != 1 {
		t.Fatalf("nudges=%d context=%+v", nudges, tc)
	}
}

// A real successful reaction already owns an audit row, not a chat message.
// Its failure or an action on a foreign turn cannot satisfy this turn.
func TestCheckTurnReadsSocialDelivery(t *testing.T) {
	for _, kind := range []string{"like", "failed", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			db, dl, srv := socialSrv(t)
			if err := db.SetTurnAddressed("t1", true); err != nil {
				t.Fatal(err)
			}
			if kind == "like" {
				bound := socialTurn()
				sock := t.TempDir() + "/gated.sock"
				stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
				if err != nil {
					t.Fatal(err)
				}
				defer stop()
				if _, msg := callToolOverSock(t, sock, "like", map[string]any{"chatJid": "slack:T/C/U", "targetId": "p9", "reaction": "👍"}); msg != "" {
					t.Fatal(msg)
				}
				if dl.reacts != 1 || countBots(t, db, "slack:T/C/U") != 0 {
					t.Fatal("reaction changed the chat-message model")
				}
			} else {
				turn, outcome := "t1", audit.OutcomeError
				if kind == "foreign" {
					turn, outcome = "other", audit.OutcomeOK
				}
				if _, err := audit.EmitTo(context.Background(), db.Obj(), audit.Event{Category: audit.CategoryAgent, Action: "message.like", Actor: "folder:demo", Folder: "demo", TurnID: turn, Outcome: outcome}); err != nil {
					t.Fatal(err)
				}
			}
			check, err := srv.mcpCheckTurn("t1")
			if err != nil || check.Delivered != (kind == "like") || (check.Nudge != "") != (kind != "like") {
				t.Fatalf("check=%+v err=%v", check, err)
			}
		})
	}
}

// The hidden turn check cannot name another turn through a bound MCP socket.
func TestCheckTurnSocketBinding(t *testing.T) {
	srv := &Server{}
	fns := srv.buildRoutdFns(turnMCP{turnID: "bound"})
	if _, err := fns.CheckTurn("demo", "other"); err == nil {
		t.Fatal("foreign turn accepted")
	}
}

// An owed success is held before recording. A reply on the resumed transcript
// closes the same turn, and a duplicate submission sends nothing again.
func TestSubmitTurnHoldsBeforeRecording(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dl := &recDeliverer{}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", "tg:1", "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnAddressed("t1", true); err != nil {
		t.Fatal(err)
	}
	req := apiv1.TurnResult{TurnID: "t1", SessionID: "s1", Status: "success"}
	ack, err := srv.recordTurnResult("t1", req)
	if err != nil || ack.Nudge == "" {
		t.Fatalf("ack=%+v err=%v", ack, err)
	}
	tc, _ := db.GetTurnContext("t1")
	if db.TurnResultRecorded("demo", "t1") || tc.State != store.TurnRunning || tc.Nudged != 1 {
		t.Fatalf("held turn was recorded or closed: %+v", tc)
	}
	if len(dl.sends) != 0 {
		t.Fatalf("premature delivery=%+v", dl.sends)
	}
	srv.replyForTest(t, "t1", "tg:1", "the answer")
	for range 2 {
		ack, err = srv.recordTurnResult("t1", req)
		if err != nil || ack.Nudge != "" {
			t.Fatalf("ack=%+v err=%v", ack, err)
		}
	}
	tc, _ = db.GetTurnContext("t1")
	if !db.TurnResultRecorded("demo", "t1") || tc.State != store.TurnDone || len(dl.sends) != 1 || db.SessionID("demo", "") != "s1" {
		t.Fatalf("completion tc=%+v sends=%+v", tc, dl.sends)
	}
}

// Stop and submission spend the same nudge. Error and timeout outcomes do not
// ask the model for an unbounded summary; ambient and isolated silence stay quiet.
func TestSubmitTurnCompletionOutcomes(t *testing.T) {
	for _, tt := range []struct {
		name, status, trigger, notice            string
		addressed, progress, stopCheck, timedOut bool
	}{
		{name: "addressed silent", status: "success", addressed: true, stopCheck: true, notice: silentTurnNotice},
		{name: "status silent", status: "success", progress: true, stopCheck: true, notice: silentTurnNotice},
		{name: "ambient", status: "success"},
		{name: "isolated", status: "success", trigger: "timed-isolated:cron"},
		{name: "error", status: "error", addressed: true, progress: true},
		{name: "timeout", status: "success", addressed: true, timedOut: true, notice: timedOutNotice},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			dl := &recDeliverer{}
			srv := NewServer(db, nil, dl, nil, 0, "")
			if _, err := db.PutTurnContext("t1", "demo", "", "tg:1", tt.trigger, ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnAddressed("t1", tt.addressed); err != nil {
				t.Fatal(err)
			}
			if tt.progress {
				if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
					t.Fatal(err)
				}
			}
			if tt.stopCheck {
				check, err := srv.mcpCheckTurn("t1")
				if err != nil || check.Nudge == "" {
					t.Fatalf("check=%+v err=%v", check, err)
				}
			}
			req := apiv1.TurnResult{TurnID: "t1", SessionID: "s1", Status: tt.status, TimedOut: tt.timedOut}
			for range 2 {
				ack, err := srv.recordTurnResult("t1", req)
				if err != nil || ack.Nudge != "" {
					t.Fatalf("ack=%+v err=%v", ack, err)
				}
			}
			var notices int
			for _, send := range dl.sends {
				if send.text == tt.notice && tt.notice != "" {
					notices++
				}
			}
			want := 0
			if tt.notice != "" {
				want = 1
			}
			if notices != want {
				t.Fatalf("notices=%d want=%d sends=%+v", notices, want, dl.sends)
			}
			tc, _ := db.GetTurnContext("t1")
			if !db.TurnResultRecorded("demo", "t1") || tc.State != store.TurnDone {
				t.Fatalf("not completed: %+v", tc)
			}
			if tt.progress {
				row, ok := db.LastStatusRow("tg:1", "t1")
				if !ok || row.Content != "✗ working" {
					t.Fatalf("progress=%+v exists=%v", row, ok)
				}
			}
			if tt.trigger != "" && db.SessionID("demo", "") != "" {
				t.Fatal("isolated lineage overwritten")
			}
		})
	}
}
