package tests

import (
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// Cross-service contract tests: the raw SQL a non-owner service would run
// against routd's scheduled_tasks and messages, checked against the schema
// routd's own chain builds. routd owns the tables and migrates them; a reader
// here runs statements only.

// timedMessage is the row timed's fire posts to routd's POST /v1/messages: a
// trigger from the scheduler, with the sender that names it. timed opens no
// database of its own, so this writes through the same door routd's handler
// does.
func timedMessage(t *testing.T, s *store.Store, id, jid, content string, at time.Time) {
	t.Helper()
	if err := s.PutMessage(core.Message{
		ID: id, ChatJID: jid, Sender: "timed", Content: content,
		Timestamp: at, Verb: "message",
	}); err != nil {
		t.Fatal("timed message:", err)
	}
}

// openSharedDB returns routd's tables twice: through the shared store library
// six daemons hold, and as the handle a caller addresses items on directly.
func openSharedDB(t *testing.T) (*store.Store, objstore.Store) {
	t.Helper()
	s := testutils.NewRoutdStore(t)
	return s, s.Obj()
}

// The fire loop's READ is store.DueTasks, which routd serves at
// GET /v1/tasks/due and timed polls — timed opens no database of its own. A task
// written through store must come back from it carrying the cron the fire is
// scheduled by.
func TestTimedFireReadClaimsADueTask(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	if err := s.CreateTask(core.Task{
		ID: "cross-1", Owner: "main", ChatJID: "tg:100",
		Prompt: "check weather", Cron: "0 9 * * *",
		NextRun: &past, Status: "active", Created: now,
	}); err != nil {
		t.Fatal("CreateTask:", err)
	}

	due, err := s.DueTasks(time.Now())
	if err != nil {
		t.Fatal("DueTasks:", err)
	}
	if len(due) != 1 {
		t.Fatalf("expected 1 due task, got %d", len(due))
	}
	if due[0].ID != "cross-1" {
		t.Errorf("task id = %q", due[0].ID)
	}
	if due[0].Cron != "0 9 * * *" {
		t.Errorf("cron = %q, want '0 9 * * *'", due[0].Cron)
	}
}

func TestTimedInsertReadableByStore(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	timedMessage(t, s, "sched-t1-123", "tg:100", "daily report", now)

	msgs, err := s.NewMessagesForChat(t.Context(), "tg:100", "", 10)
	if err != nil {
		t.Fatal("NewMessagesForChat:", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.ID != "sched-t1-123" || m.ChatJID != "tg:100" || m.Sender != "timed" || m.Content != "daily report" {
		t.Errorf("msg = %+v", m)
	}
}

// timed sets sender='timed'. What decides whether a row is the agent's own
// output is the is_bot_message FLAG and nothing else — no name is inspected —
// so a scheduler row whose sender merely LOOKS like a bot name is still a
// trigger the dispatch batch keeps.
func TestTimedMessagesAreNotTheAgentsOwnOutput(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	timedMessage(t, s, "sched-f1", "tg:300", "scheduled msg", now)
	if err := s.PutMessage(core.Message{
		ID: "sched-f2", ChatJID: "tg:300", Sender: "timed-bot", Content: "also a trigger",
		Timestamp: now.Add(time.Second), Verb: "message",
	}); err != nil {
		t.Fatal(err)
	}

	msgs, err := s.NewMessagesForChat(t.Context(), "tg:300", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 msgs, got %d", len(msgs))
	}
	for _, m := range msgs {
		if m.BotMsg {
			t.Errorf("%s reads as the agent's own output; only the flag decides that", m.ID)
		}
	}
}

func TestTimedMultipleMessagesOrdered(t *testing.T) {
	s, _ := openSharedDB(t)

	base := time.Now()
	for i := range 5 {
		timedMessage(t, s, fmt.Sprintf("sched-ord-%d", i), "tg:400",
			fmt.Sprintf("msg-%d", i), base.Add(time.Duration(i)*time.Second))
	}

	msgs, err := s.NewMessagesForChat(t.Context(), "tg:400", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(msgs))
	}
	for i, m := range msgs {
		want := fmt.Sprintf("msg-%d", i)
		if m.Content != want {
			t.Errorf("msg[%d] = %q, want %q", i, m.Content, want)
		}
	}
}

func TestTaskNextRunUpdate(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "nextrun-1", Owner: "main", ChatJID: "tg:600",
		Prompt: "update test", Cron: "0 9 * * *",
		NextRun: &past, Status: "active", Created: now,
	})

	future := now.Add(24 * time.Hour).Format(time.RFC3339)
	if err := s.RescheduleTask("nextrun-1", future, core.TaskActive); err != nil {
		t.Fatal("reschedule:", err)
	}

	got, ok := s.GetTask("nextrun-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.NextRun == nil || !got.NextRun.After(now) {
		t.Errorf("next_run %v should be in the future", got.NextRun)
	}
}

// TestCronFiresMessage exercises the timed → gated contract: insert an
// active scheduled_task with next_run in the past, simulate timed's
// atomic claim + message insert, and verify gated's store sees the row
// appear in the messages table carrying the task's prompt.
func TestCronFiresMessage(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Second)
	cronExpr := "* * * * *"
	if err := s.CreateTask(core.Task{
		ID: "fires-1", Owner: "main", ChatJID: "tg:500",
		Prompt: "daily standup", Cron: cronExpr, NextRun: &past,
		Status: "active", Created: now,
	}); err != nil {
		t.Fatal("CreateTask:", err)
	}

	// Replicate timed's fire: claim the due tasks through the read routd serves
	// at GET /v1/tasks/due, then insert a message per claimed task. The messages
	// loop consumes the inserted rows.
	claimed, err := s.DueTasks(time.Now())
	if err != nil {
		t.Fatal("claim:", err)
	}
	for _, task := range claimed {
		timedMessage(t, s, "sched-"+task.ID, task.ChatJID, task.Prompt, time.Now())
	}
	if len(claimed) != 1 {
		t.Fatalf("fired = %d, want 1", len(claimed))
	}

	msgs, err := s.NewMessagesForChat(t.Context(), "tg:500", "", 10)
	if err != nil {
		t.Fatal("NewMessagesForChat:", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages for tg:500 = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "daily standup" {
		t.Errorf("content = %q, want %q", msgs[0].Content, "daily standup")
	}
	if msgs[0].Sender != "timed" {
		t.Errorf("sender = %q, want timed", msgs[0].Sender)
	}
}

func TestOneShotTaskNullsNextRun(t *testing.T) {
	s, _ := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "oneshot-1", Owner: "main", ChatJID: "tg:700",
		Prompt:  "run once",
		NextRun: &past, Status: "active", Created: now,
	})

	// A one-shot that has fired carries NO next_run: an empty reschedule takes
	// the attribute away, and absent is the only null this model has.
	if err := s.RescheduleTask("oneshot-1", "", core.TaskActive); err != nil {
		t.Fatal("reschedule:", err)
	}

	got, ok := s.GetTask("oneshot-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.NextRun != nil {
		t.Errorf("next_run should be nil, got %v", got.NextRun)
	}

	if n := dueCount(t, s); n != 0 {
		t.Errorf("one-shot task still due: count=%d", n)
	}
}
