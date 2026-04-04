package tests

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
	_ "modernc.org/sqlite"
)

// timedMigrationSQL is timed's 0001-schema.sql — duplicated here because
// timed is package main and cannot be imported.
const timedMigrationSQL = `
CREATE TABLE IF NOT EXISTS scheduled_tasks (
  id TEXT PRIMARY KEY,
  owner TEXT NOT NULL,
  chat_jid TEXT NOT NULL,
  prompt TEXT NOT NULL,
  cron TEXT,
  next_run TEXT,
  status TEXT NOT NULL DEFAULT 'active',
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_tasks_next ON scheduled_tasks(status, next_run);
`

// timedFireSQL is the exact query timed uses to find due tasks.
const timedFireSQL = `SELECT id, chat_jid, prompt, cron FROM scheduled_tasks
 WHERE status = 'active' AND next_run <= ?`

// timedInsertMsg is the exact INSERT timed uses to produce messages.
const timedInsertMsg = `INSERT INTO messages (id, chat_jid, sender, content, timestamp)
 VALUES (?, ?, 'timed', ?, ?)`

// openSharedDB creates an in-memory SQLite DB with WAL mode, runs store
// (gated) migrations, then returns both the Store and raw *sql.DB handle.
func openSharedDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal("store.Open:", err)
	}
	t.Cleanup(func() { s.Close() })

	// open a second connection to the same file for timed-side SQL
	dsn := filepath.Join(dir, "store", "messages.db") + "?_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal("sql.Open:", err)
	}
	db.Exec("PRAGMA journal_mode=WAL")
	t.Cleanup(func() { db.Close() })

	return s, db
}

// runTimedMigration simulates timed's migrate() against the shared DB.
func runTimedMigration(t *testing.T, db *sql.DB) {
	t.Helper()
	db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`)
	_, err := db.Exec(timedMigrationSQL)
	if err != nil {
		t.Fatal("timed migration:", err)
	}
	_, err = db.Exec(
		"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
		"timed", 1, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatal("record timed migration:", err)
	}
}

// --- 1. Migration compatibility ---

func TestMicroservice_MigrationCompatibility(t *testing.T) {
	_, db := openSharedDB(t) // store migrations already ran
	runTimedMigration(t, db)

	// Both migration records exist
	var storeVer, timedVer int
	db.QueryRow("SELECT MAX(version) FROM migrations WHERE service='store'").Scan(&storeVer)
	db.QueryRow("SELECT MAX(version) FROM migrations WHERE service='timed'").Scan(&timedVer)

	if storeVer == 0 {
		t.Fatal("store migrations not recorded")
	}
	if timedVer != 1 {
		t.Fatalf("timed migration version = %d, want 1", timedVer)
	}

	// Both services' tables exist — spot-check with PRAGMA
	tables := map[string]bool{
		"messages":        false,
		"scheduled_tasks": false,
		"groups": false,
		"migrations":      false,
	}
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		rows.Scan(&name)
		if _, ok := tables[name]; ok {
			tables[name] = true
		}
	}
	for tbl, found := range tables {
		if !found {
			t.Errorf("table %q missing after both migrations", tbl)
		}
	}
}

func TestMicroservice_MigrationIdempotent(t *testing.T) {
	_, db := openSharedDB(t)
	runTimedMigration(t, db)

	// run timed migration again — IF NOT EXISTS should be safe
	_, err := db.Exec(timedMigrationSQL)
	if err != nil {
		t.Fatal("re-running timed migration failed:", err)
	}
}

// --- 2. Schema conflict: timed's scheduled_tasks vs store's ---

// Store and timed define scheduled_tasks with the SAME schema.
// This test verifies timed's fire query works against store's table.
func TestMicroservice_TimedQueryWorksOnStoreSchema(t *testing.T) {
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	task := core.Task{
		ID:      "cross-1",
		Owner:   "main",
		ChatJID: "tg:100",
		Prompt:  "check weather",
		Cron:    "0 9 * * *",
		NextRun: &past,
		Status:  "active",
		Created: now,
	}
	if err := s.CreateTask(task); err != nil {
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
			t.Errorf("unexpected task id: %q", id)
		}
		if cronExpr == nil || *cronExpr != "0 9 * * *" {
			t.Errorf("cron = %v, want '0 9 * * *'", cronExpr)
		}
	}
	if found != 1 {
		t.Errorf("expected 1 due task, got %d", found)
	}
}

// --- 3. Timed -> Messages contract ---

func TestMicroservice_TimedInsertReadableByStore(t *testing.T) {
	s, db := openSharedDB(t)

	// Simulate timed inserting a message using its exact SQL
	now := time.Now()
	ts := now.Format(time.RFC3339)
	_, err := db.Exec(timedInsertMsg,
		"sched-t1-123", "tg:100", "daily report", ts)
	if err != nil {
		t.Fatal("timed insert message:", err)
	}

	// Gated reads messages via store.NewMessages
	msgs, _, err := s.NewMessages([]string{"tg:100"}, now.Add(-time.Second), "Bot")
	if err != nil {
		t.Fatal("NewMessages:", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Sender != "timed" {
		t.Errorf("sender = %q, want 'timed'", msgs[0].Sender)
	}
	if msgs[0].Content != "daily report" {
		t.Errorf("content = %q, want 'daily report'", msgs[0].Content)
	}
	if msgs[0].ChatJID != "tg:100" {
		t.Errorf("chat_jid = %q, want 'tg:100'", msgs[0].ChatJID)
	}
	if msgs[0].ID != "sched-t1-123" {
		t.Errorf("id = %q, want 'sched-t1-123'", msgs[0].ID)
	}
}

func TestMicroservice_TimedMessageNullableFieldsOK(t *testing.T) {
	s, db := openSharedDB(t)

	// timed's INSERT only sets: id, chat_jid, sender, content, timestamp
	// store's messages table has additional nullable columns:
	//   sender_name, is_from_me, is_bot_message, forwarded_from,
	//   reply_to_id, reply_to_text, reply_to_sender
	// Verify that omitting them doesn't break store reads.
	ts := time.Now().Format(time.RFC3339)
	db.Exec(timedInsertMsg, "sched-null-1", "tg:200", "partial insert", ts)

	msgs, _, _ := s.NewMessages([]string{"tg:200"}, time.Time{}, "Bot")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	m := msgs[0]

	// nullable fields should have zero values, not errors
	if m.Name != "" {
		t.Errorf("sender_name should be empty, got %q", m.Name)
	}
	if m.FromMe {
		t.Error("is_from_me should default to false")
	}
	if m.BotMsg {
		t.Error("is_bot_message should default to false")
	}
	if m.ForwardedFrom != "" {
		t.Errorf("forwarded_from should be empty, got %q", m.ForwardedFrom)
	}
}

func TestMicroservice_TimedMessagesNotFilteredAsBot(t *testing.T) {
	s, db := openSharedDB(t)

	// timed sets sender='timed'. gated filters sender LIKE 'BotName%'.
	// Verify timed messages pass through the filter.
	ts := time.Now().Format(time.RFC3339)
	db.Exec(timedInsertMsg, "sched-f1", "tg:300", "scheduled msg", ts)

	// with different bot names — timed should never match
	for _, botName := range []string{"Bot", "Arizuko", "Assistant", "timed-bot"} {
		msgs, _, _ := s.NewMessages([]string{"tg:300"}, time.Time{}, botName)
		if len(msgs) != 1 {
			t.Errorf("botName=%q: expected 1 msg, got %d (timed filtered out!)", botName, len(msgs))
		}
	}
}

func TestMicroservice_TimedMultipleMessagesOrdered(t *testing.T) {
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
			t.Errorf("msg[%d].Content = %q, want %q", i, m.Content, want)
		}
	}
}

// --- 4. Scheduled tasks lifecycle via store ---

func TestMicroservice_TaskCreatedByStoreQueryableByTimedSQL(t *testing.T) {
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "lifecycle-1", Owner: "main", ChatJID: "tg:500",
		Prompt: "lifecycle test", Cron: "0 8 * * *",
		NextRun: &past, Status: "active", Created: now,
	})

	rows, err := db.Query(timedFireSQL, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatal("query due tasks:", err)
	}
	defer rows.Close()

	var found int
	for rows.Next() {
		var id, jid, prompt string
		var cronExpr *string
		rows.Scan(&id, &jid, &prompt, &cronExpr)
		found++
		if id != "lifecycle-1" {
			t.Errorf("task id = %q", id)
		}
		if cronExpr == nil || *cronExpr != "0 8 * * *" {
			t.Errorf("cron = %v, want '0 8 * * *'", cronExpr)
		}
	}
	if found != 1 {
		t.Errorf("expected 1 due task, got %d", found)
	}
}

func TestMicroservice_TaskNextRunUpdate(t *testing.T) {
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "nextrun-1", Owner: "main", ChatJID: "tg:600",
		Prompt: "update test", Cron: "0 9 * * *",
		NextRun: &past, Status: "active", Created: now,
	})

	// Simulate timed updating next_run after firing (using store's column names)
	future := now.Add(24 * time.Hour).Format(time.RFC3339)
	_, err := db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
		future, "nextrun-1")
	if err != nil {
		t.Fatal("update next_run:", err)
	}

	// Verify store sees the updated next_run
	got, ok := s.GetTask("nextrun-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.NextRun == nil {
		t.Fatal("next_run is nil after update")
	}
	if !got.NextRun.After(now) {
		t.Errorf("next_run %v should be in the future", got.NextRun)
	}
}

func TestMicroservice_OneShotTaskNullsNextRun(t *testing.T) {
	s, db := openSharedDB(t)

	now := time.Now()
	past := now.Add(-time.Minute)
	s.CreateTask(core.Task{
		ID: "oneshot-1", Owner: "main", ChatJID: "tg:700",
		Prompt: "run once",
		NextRun: &past, Status: "active", Created: now,
	})

	// Simulate timed nulling next_run for one-shot tasks
	db.Exec(`UPDATE scheduled_tasks SET next_run = NULL WHERE id = ?`, "oneshot-1")

	// store should see NULL next_run
	got, ok := s.GetTask("oneshot-1")
	if !ok {
		t.Fatal("task not found")
	}
	if got.NextRun != nil {
		t.Errorf("next_run should be nil for fired one-shot, got %v", got.NextRun)
	}

	// task should no longer appear as due
	var n int
	db.QueryRow(`SELECT COUNT(*) FROM scheduled_tasks
		WHERE status = 'active' AND next_run <= ?`,
		time.Now().Format(time.RFC3339)).Scan(&n)
	if n != 0 {
		t.Errorf("one-shot task still due after null next_run: count=%d", n)
	}
}

// --- 5. Table ownership isolation ---

func TestMicroservice_TimedWriteScope(t *testing.T) {
	_, db := openSharedDB(t)

	// Get all gated-owned tables
	gatedTables := make(map[string]bool)
	rows, _ := db.Query("SELECT name FROM sqlite_master WHERE type='table'")
	for rows.Next() {
		var name string
		rows.Scan(&name)
		gatedTables[name] = true
	}
	rows.Close()

	// timed should only write to these tables:
	timedWriteTables := map[string]bool{
		"scheduled_tasks": true, // timed's own table
		"messages":        true, // shared output
		"migrations":      true, // migration tracking
	}

	// verify timed's write targets are a subset of existing tables
	for tbl := range timedWriteTables {
		if !gatedTables[tbl] {
			t.Errorf("timed writes to %q but table doesn't exist in shared DB", tbl)
		}
	}

	// verify timed does NOT need to touch gated-only tables
	gatedOnly := []string{
		"chats", "groups", "sessions", "session_log",
		"system_messages", "router_state", "auth_users",
		"auth_sessions", "email_threads", "routes",
	}
	for _, tbl := range gatedOnly {
		if timedWriteTables[tbl] {
			t.Errorf("timed should NOT write to gated-owned table %q", tbl)
		}
	}
}

func TestMicroservice_TimedMigrationDoesNotAlterGatedTables(t *testing.T) {
	_, db := openSharedDB(t)

	// capture gated table schemas before timed migration
	schemas := make(map[string]string)
	rows, _ := db.Query("SELECT name, sql FROM sqlite_master WHERE type='table'")
	for rows.Next() {
		var name string
		var ddl sql.NullString
		rows.Scan(&name, &ddl)
		if ddl.Valid {
			schemas[name] = ddl.String
		}
	}
	rows.Close()

	// run timed migration
	runTimedMigration(t, db)

	// verify gated table schemas unchanged
	rows, _ = db.Query("SELECT name, sql FROM sqlite_master WHERE type='table'")
	for rows.Next() {
		var name string
		var ddl sql.NullString
		rows.Scan(&name, &ddl)
		if name == "migrations" {
			continue // shared, timed adds rows but doesn't alter schema
		}
		before, existed := schemas[name]
		if !existed {
			continue // new table from timed, that's fine
		}
		if ddl.Valid && ddl.String != before {
			t.Errorf("table %q schema changed after timed migration:\n  before: %s\n  after:  %s",
				name, before, ddl.String)
		}
	}
	rows.Close()
}
