package routd

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

func TestProgress_ReminderCountsParentCalls(t *testing.T) {
	db, srv := newProgressTurn(t, &recDeliverer{}, "")
	if err := db.SetTurnAddressed("t1", false); err != nil {
		t.Fatal(err)
	}
	send := func(id, agent, event string) ipc.TurnStatusAck {
		t.Helper()
		raw := fmt.Sprintf(`{"hook_event_name":%q,"tool_name":"Read","session_id":"s","tool_use_id":%q,"agent_id":%q,"tool_input":{},"tool_response":{"success":false}}`, event, id, agent)
		ack, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Source: "claude", Snapshot: []byte(raw)})
		if err != nil {
			t.Fatal(err)
		}
		return ack
	}
	for i := 0; i < 7; i++ {
		if send(fmt.Sprint(i), "", "PostToolUse").Remind {
			t.Fatal("early reminder")
		}
	}
	if send("0", "", "PostToolUse").Remind || send("child", "child", "PostToolUse").Remind {
		t.Fatal("duplicate or child counted")
	}
	srv = NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	if !send("8", "", "PostToolUseFailure").Remind {
		t.Fatal("eighth completion did not remind")
	}
	if send("9", "", "PostToolUse").Remind {
		t.Fatal("repeated reminder")
	}
	if len(statusRows(t, db, progressJID)) != 0 {
		t.Fatal("silent turn published")
	}
}

func TestProgress_ReminderTimeAndChecklist(t *testing.T) {
	start := time.Now()
	progress := &turnProgress{}
	if ack := progress.reminder(start, start.Add(179*time.Second)); ack.Remind || ack.ReminderAfterMS == nil || *ack.ReminderAfterMS != 1000 {
		t.Fatalf("early: %+v", ack)
	}
	if !progress.reminder(start, start.Add(180*time.Second)).Remind {
		t.Fatal("time did not remind")
	}
	progress.calls = map[string]bool{"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true, "8": true}
	if progress.reminder(start, start.Add(181*time.Second)).Remind {
		t.Fatal("second trigger reminded")
	}
	progress = &turnProgress{active: true, calls: progress.calls}
	if progress.reminder(start, start.Add(time.Hour)).Remind {
		t.Fatal("checklist reminded")
	}
}

func TestProgress_ReminderRetainsTurnStateAndSuppressesClearedChecklist(t *testing.T) {
	db, srv := newProgressTurn(t, &recDeliverer{}, "")
	for i := 0; i < 7; i++ {
		raw := fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"Read","session_id":"s","tool_use_id":%q,"tool_input":{}}`, fmt.Sprint(i))
		ack, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Source: "claude", Snapshot: []byte(raw)})
		if err != nil || ack.Remind {
			t.Fatalf("call %d: %+v %v", i, ack, err)
		}
	}
	srv = NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	if _, err := srv.submitStatus(snapshot("list", "milestone")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("clear")); err != nil {
		t.Fatal(err)
	}
	raw := `{"hook_event_name":"PostToolUse","tool_name":"Read","session_id":"s","tool_use_id":"8","tool_input":{}}`
	ack, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Source: "claude", Snapshot: []byte(raw)})
	if err != nil || ack.Remind || ack.ReminderAfterMS != nil {
		t.Fatalf("cleared checklist: %+v %v", ack, err)
	}
	if _, err := db.PutTurnContext("t2", "demo", "", progressJID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	ack, err = srv.submitStatus(ipc.TurnStatus{TurnID: "t2"})
	if err != nil || ack.Remind || ack.ReminderAfterMS == nil {
		t.Fatalf("new turn: %+v %v", ack, err)
	}
}

func TestProgress_ReminderSurvivesFallbackEditFailure(t *testing.T) {
	dl := &recDeliverer{}
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.NewOver(db.Obj()).SeedItem(t.Context(), store.TurnContexts, []string{"t1"}, map[string]string{"folder": "demo", "chat_jid": progressJID, "state": "running", "started_at": core.TS(time.Now().Add(-4 * time.Minute)), "addressed": "1"}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "first"}); err != nil {
		t.Fatal(err)
	}
	value, _ := db.progress.Load("t1")
	progress := value.(*turnProgress)
	progress.lastAttempt = time.Now().Add(-progressInterval)
	progress.lastPublished = progress.lastAttempt
	dl.editErr = errSend
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "second"}); err == nil {
		t.Fatal("expected edit error")
	}
	ack, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1"})
	if err != nil || !ack.Remind {
		t.Fatalf("reminder lost: %+v %v", ack, err)
	}
}

func TestProgress_ReminderTimeAndCallRace(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.NewOver(db.Obj()).SeedItem(t.Context(), store.TurnContexts, []string{"t1"}, map[string]string{"folder": "demo", "chat_jid": progressJID, "state": "running", "started_at": core.TS(time.Now().Add(-4 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	db.progress.Store("t1", &turnProgress{calls: map[string]bool{"1": true, "2": true, "3": true, "4": true, "5": true, "6": true, "7": true}, sources: make(map[string][]progressItem), seen: make(map[string]bool)})
	raw := `{"hook_event_name":"PostToolUse","tool_name":"Read","session_id":"s","tool_use_id":"8","tool_input":{}}`
	results := make(chan ipc.TurnStatusAck, 2)
	var group sync.WaitGroup
	for _, status := range []ipc.TurnStatus{{TurnID: "t1"}, {TurnID: "t1", Source: "claude", Snapshot: []byte(raw)}} {
		group.Add(1)
		go func() {
			defer group.Done()
			ack, err := srv.submitStatus(status)
			if err != nil {
				t.Error(err)
			}
			results <- ack
		}()
	}
	group.Wait()
	close(results)
	count := 0
	for ack := range results {
		if ack.Remind {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("reminders = %d", count)
	}
}

func TestProgress_ExpiredTurnDropsStatusAndReminder(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	if err := db.SetTurnState("t1", store.TurnExpired); err != nil {
		t.Fatal(err)
	}
	ack, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "late"})
	if err != nil || ack.Remind || len(dl.sends) != 0 {
		t.Fatalf("expired callback: %+v %v sends=%+v", ack, err, dl.sends)
	}
	if _, ok := db.progress.Load("t1"); ok {
		t.Fatal("expired callback created state")
	}
}
