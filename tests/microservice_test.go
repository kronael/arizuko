package tests

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

// Cross-service contract tests: the raw SQL a non-owner service would run
// against routd's scheduled_tasks and messages, checked against the schema
// routd's own chain builds. routd owns the tables and migrates them; a reader
// here runs statements only.

const timedInsertMsg = `INSERT INTO messages (id, chat_jid, sender, content, timestamp)
 VALUES (?, ?, 'timed', ?, ?)`

// openSharedDB returns routd.db's schema twice: through the shared store library
// six daemons hold, and as the raw *sql.DB a caller runs statements on directly.
func openSharedDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	s := testutils.NewRoutdStore(t)
	return s, s.DB()
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
	s, db := openSharedDB(t)

	now := time.Now()
	ts := now.Format(time.RFC3339)
	if _, err := db.Exec(timedInsertMsg, "sched-t1-123", "tg:100", "daily report", ts); err != nil {
		t.Fatal("timed insert message:", err)
	}

	msgs, _, err := s.NewMessages([]string{"tg:100"}, now.Add(-time.Second), "Bot")
	if err != nil {
		t.Fatal("NewMessages:", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]
	if m.ID != "sched-t1-123" || m.ChatJID != "tg:100" || m.Sender != "timed" || m.Content != "daily report" {
		t.Errorf("msg = %+v", m)
	}
}

// timed sets sender='timed'. gated filters sender LIKE '<botName>%'.
// Verify timed messages pass through regardless of bot name.
func TestTimedMessagesNotFilteredAsBot(t *testing.T) {
	s, db := openSharedDB(t)

	ts := time.Now().Format(time.RFC3339)
	db.Exec(timedInsertMsg, "sched-f1", "tg:300", "scheduled msg", ts)

	for _, botName := range []string{"Bot", "Arizuko", "Assistant", "timed-bot"} {
		msgs, _, _ := s.NewMessages([]string{"tg:300"}, time.Time{}, botName)
		if len(msgs) != 1 {
			t.Errorf("botName=%q: expected 1 msg, got %d", botName, len(msgs))
		}
	}
}

func TestTimedMultipleMessagesOrdered(t *testing.T) {
	s, db := openSharedDB(t)

	base := time.Now()
	for i := range 5 {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		db.Exec(timedInsertMsg, fmt.Sprintf("sched-ord-%d", i), "tg:400", fmt.Sprintf("msg-%d", i), ts)
	}

	msgs, _, _ := s.NewMessages([]string{"tg:400"}, time.Time{}, "Bot")
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
	s, db := openSharedDB(t)

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
		if _, err := db.Exec(timedInsertMsg,
			"sched-"+task.ID, task.ChatJID, task.Prompt, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal("insert msg:", err)
		}
	}
	if len(claimed) != 1 {
		t.Fatalf("fired = %d, want 1", len(claimed))
	}

	msgs, _, err := s.NewMessages([]string{"tg:500"}, time.Time{}, "Bot")
	if err != nil {
		t.Fatal("NewMessages:", err)
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
