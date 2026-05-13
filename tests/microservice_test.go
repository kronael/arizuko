package tests

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// Cross-service contract tests: verify timed's raw SQL stays compatible
// with the schema gated owns (store/migrations/). Gated owns all schema;
// timed connects to the already-migrated DB and never runs migrations.

const timedFireSQL = `SELECT id, chat_jid, prompt, cron FROM scheduled_tasks
 WHERE status = 'active' AND next_run <= ?`

const timedInsertMsg = `INSERT INTO messages (id, chat_jid, sender, content, timestamp)
 VALUES (?, ?, 'timed', ?, ?)`

// openSharedDB opens a store (runs migrations) and returns a second *sql.DB
// handle for raw timed-style queries against the same file.
func openSharedDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal("store.Open:", err)
	}
	t.Cleanup(func() { s.Close() })

	dsn := filepath.Join(dir, "store", "messages.db") + "?_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal("sql.Open:", err)
	}
	db.Exec("PRAGMA journal_mode=WAL")
	t.Cleanup(func() { db.Close() })

	return s, db
}

func TestTimedFireQueryWorksOnStoreSchema(t *testing.T) {
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	if err := s.CreateTask(core.Task{
		ID: "cross-1", Owner: "main", ChatJID: "tg:100",
		Prompt: "check weather", Cron: "0 9 * * *",
		NextRun: &past, Status: "active", Created: now,
	}); err != nil {
		t.Fatal("CreateTask:", err)
	}

	rows, err := db.Query(timedFireSQL, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatal("timed fire query:", err)
	}
	defer rows.Close()

	var found int
	for rows.Next() {
		var id, jid, prompt string
		var cronExpr *string
		rows.Scan(&id, &jid, &prompt, &cronExpr)
		found++
		if id != "cross-1" {
			t.Errorf("task id = %q", id)
		}
		if cronExpr == nil || *cronExpr != "0 9 * * *" {
			t.Errorf("cron = %v, want '0 9 * * *'", cronExpr)
		}
	}
	if found != 1 {
		t.Errorf("expected 1 due task, got %d", found)
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
	for i := 0; i < 5; i++ {
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
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "nextrun-1", Owner: "main", ChatJID: "tg:600",
		Prompt: "update test", Cron: "0 9 * * *",
		NextRun: &past, Status: "active", Created: now,
	})

	future := now.Add(24 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`, future, "nextrun-1"); err != nil {
		t.Fatal("update next_run:", err)
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

	// Replicate timed.fire(): atomic claim due rows then insert a message
	// per claimed task. gated's messages loop consumes inserted rows.
	claimAt := time.Now().Format(time.RFC3339)
	if _, err := db.Exec(
		`UPDATE scheduled_tasks SET status='firing'
		 WHERE status='active' AND next_run <= ?`, claimAt,
	); err != nil {
		t.Fatal("claim:", err)
	}
	rows, err := db.Query(
		`SELECT id, chat_jid, prompt FROM scheduled_tasks WHERE status='firing'`)
	if err != nil {
		t.Fatal("select firing:", err)
	}
	defer rows.Close()
	var fired int
	for rows.Next() {
		var id, jid, prompt string
		if err := rows.Scan(&id, &jid, &prompt); err != nil {
			t.Fatal("scan:", err)
		}
		fired++
		if _, err := db.Exec(timedInsertMsg,
			"sched-"+id, jid, prompt, time.Now().Format(time.RFC3339),
		); err != nil {
			t.Fatal("insert msg:", err)
		}
	}
	if fired != 1 {
		t.Fatalf("fired = %d, want 1", fired)
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
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "oneshot-1", Owner: "main", ChatJID: "tg:700",
		Prompt: "run once",
		NextRun: &past, Status: "active", Created: now,
	})

	db.Exec(`UPDATE scheduled_tasks SET next_run = NULL WHERE id = ?`, "oneshot-1")

	got, ok := s.GetTask("oneshot-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.NextRun != nil {
		t.Errorf("next_run should be nil, got %v", got.NextRun)
	}

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks
		WHERE status = 'active' AND next_run <= ?`,
		time.Now().Format(time.RFC3339)).Scan(&n)
	if n != 0 {
		t.Errorf("one-shot task still due: count=%d", n)
	}
}
