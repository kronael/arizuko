package routd

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

func TestModelPauseDueProbeKeepsOtherChatNotice(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	pauseInput(t, db, "web:a", "old", "u", "first", now)
	processPauseChat(t, l, "web:a")
	pauseInput(t, db, "web:b", "later", "v", "second", now.Add(time.Second))
	modelPauseDue(t, l)
	for range 2 {
		processPauseChat(t, l, "web:b")
	}
	rows, err := db.MessagesSince("web:b", "")
	if err != nil {
		t.Fatal(err)
	}
	notices := 0
	for _, row := range rows {
		if strings.HasPrefix(row.ID, "model-pause-") {
			notices++
		}
	}
	if len(p.runs) != 1 || notices != 1 || db.GetAgentCursor("web:b") != "" {
		t.Fatalf("due pause: runs=%d notices=%d cursor=%q", len(p.runs), notices, db.GetAgentCursor("web:b"))
	}
}

func TestModelPauseOrderedCommandKeepsInput(t *testing.T) {
	db, l, p, _ := pauseFixture(t, "rate_limit")
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	pauseInput(t, db, "web:a", "held", "u", "saved", now)
	processPauseChat(t, l, "web:a")
	pause, err := db.store().ReadModelPause(t.Context(), "a")
	if err != nil {
		t.Fatal(err)
	}
	w := doJSON(t, p.srv.Handler(), "POST", "/v1/model_pauses/clear", "", map[string]string{"folder": "a", "opened_at": pause.OpenedAt})
	if w.Code != 200 {
		t.Fatalf("clear=%d %s", w.Code, w.Body.String())
	}
	processPauseChat(t, l, "web:a")
	before := db.GetAgentCursor("web:a")
	pauseInput(t, db, "web:b", "older", "v", "first", now.Add(time.Second))
	pauseInput(t, db, "web:a", "question", "u", "second", now.Add(2*time.Second))
	pauseInput(t, db, "web:a", "command", "u", "/new", now.Add(3*time.Second))
	processPauseChat(t, l, "web:a")
	if cursor := db.GetAgentCursor("web:a"); cursor != before {
		t.Fatalf("command crossed waiting input: cursor=%q, want %q", cursor, before)
	}
	providerDue(t, l, "held")
	processPauseChat(t, l, "web:b")
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 4 || p.runs[2].TurnID != "older" || p.runs[3].TurnID != "question" {
		t.Fatalf("ordered command lost input: %+v", p.runs)
	}
}

func TestModelPauseOrderedCommandRunsOnceAfterAllBatches(t *testing.T) {
	db, l, p, dl := pauseFixture(t)
	pauseChatInFolder(t, db, "web:b", "a")
	now := time.Now()
	pauseInput(t, db, "web:a", "first", "u", "first", now)
	pauseInput(t, db, "web:b", "second", "v", "second", now.Add(time.Second))
	if err := db.PutMessage(core.Message{ChatJID: "web:a", ID: "third", Sender: "u", Content: "third", Timestamp: now.Add(2 * time.Second), Topic: "other"}); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, "web:a", "command", "u", "/new", now.Add(3*time.Second))
	processPauseChat(t, l, "web:a")
	if cursor := db.GetAgentCursor("web:a"); !strings.HasSuffix(cursor, "first") {
		t.Fatalf("command crossed later batch: cursor=%q", cursor)
	}
	processPauseChat(t, l, "web:b")
	processPauseChat(t, l, "web:a")
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 3 || p.runs[0].TurnID != "first" || p.runs[1].TurnID != "second" || p.runs[2].TurnID != "third" {
		t.Fatalf("command changed batch order: %+v", p.runs)
	}
	acks := 0
	for _, sent := range dl.sentMsgs() {
		if strings.HasPrefix(sent.idem, "ack-") {
			acks++
		}
	}
	commandCursor := store.MessageCursor(core.Message{ID: "command", Timestamp: now.Add(3 * time.Second)})
	if acks != 1 || db.GetAgentCursor("web:a") < commandCursor {
		t.Fatalf("command acknowledgements=%d cursor=%q", acks, db.GetAgentCursor("web:a"))
	}
}

func TestModelPauseOrderedRootKeepsElevation(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	pauseChatInFolder(t, db, "web:b", "a")
	if err := addMembership(db, "u", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	pauseInput(t, db, "web:b", "older", "v", "first", time.Now().Add(-time.Second))
	l.cmdRoot("web:a", "a", "root work", "u")
	rows, err := db.NewMessagesForChat("web:a", "", 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("root input: %v %v", rows, err)
	}
	processPauseChat(t, l, "web:a")
	if _, ok := l.pendingElevation.Load(rows[0].ID); !ok {
		t.Fatal("ordering refusal consumed root elevation")
	}
	processPauseChat(t, l, "web:b")
	processPauseChat(t, l, "web:a")
	if len(p.runs) != 2 || !p.runs[1].Elevated {
		t.Fatalf("root input ran without its elevation: %+v", p.runs)
	}
}

func TestModelPausePollHeldOrder(t *testing.T) {
	for _, clear := range []string{"manual", "probe"} {
		t.Run(clear, func(t *testing.T) {
			db, setup, p, dl := pauseFixture(t, "rate_limit")
			pauseChatInFolder(t, db, "web:b", "a")
			pauseChatInFolder(t, db, "web:z", "a")
			now := time.Now().UTC()
			pauseInput(t, db, "web:a", "a0", "u", "held", now)
			processPauseChat(t, setup, "web:a")
			pauseInput(t, db, "web:a", "a1", "u", "first", now.Add(time.Second))
			pauseInput(t, db, "web:z", "b", "v", "second", now.Add(2*time.Second))
			if err := db.PutGroup(core.Group{Folder: "z"}); err != nil {
				t.Fatal(err)
			}
			if err := db.PutMessage(core.Message{ChatJID: "web:a", ID: "a3", Sender: "u", Content: "third", Timestamp: now.Add(3 * time.Second), Topic: "other"}); err != nil {
				t.Fatal(err)
			}
			pauseInput(t, db, "web:b", "c", "u", "fourth", now.Add(4*time.Second))
			if clear == "manual" {
				pause, err := db.store().ReadModelPause(t.Context(), "a")
				if err != nil {
					t.Fatal(err)
				}
				ws := &store.WriteSet{}
				if err := db.store().ClearModelPauseInto(ws, *pause, errors.New("changed")); err != nil {
					t.Fatal(err)
				}
				if err := db.store().Apply(t.Context(), ws); err != nil {
					t.Fatal(err)
				}
			}
			l := NewLoop(db, p, LoopConfig{Deliver: dl, MaxRuns: 1})
			modelPauseDue(t, l)
			p.srv = NewServer(db, l, dl, nil, 0, "")
			t.Cleanup(l.StopQueue)
			// Poll and ingress use the same queue; drain each round before refeeding.
			for range 5 {
				l.pollOnce()
				for queueHasWorker(l.q) {
					runtime.Gosched()
				}
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			got := make([]string, 0, len(p.runs)-1)
			for _, run := range p.runs[1:] {
				got = append(got, run.TurnID)
			}
			want := []string{"a0", "a1", "b", "a3", "c"}
			if len(got) != len(want) {
				t.Fatalf("admission order = %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("admission order = %v, want %v", got, want)
				}
			}
		})
	}
}
