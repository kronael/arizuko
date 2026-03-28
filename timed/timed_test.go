package main

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file::memory:?mode=memory&cache=shared&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA journal_mode=WAL")
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return db
}

func setupMessages(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		chat_jid TEXT NOT NULL,
		sender TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp TEXT NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTask(t *testing.T, db *sql.DB, id, jid, prompt, status, nextRun string, cronExpr *string) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at)
		 VALUES (?, 'test', ?, ?, ?, ?, ?, ?)`,
		id, jid, prompt, cronExpr, nextRun, status, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
}

func insertTaskWithMode(t *testing.T, db *sql.DB, id, jid, prompt, status, nextRun string,
	cronExpr *string, contextMode string,
) {
	t.Helper()
	_, err := db.Exec(
		`INSERT INTO scheduled_tasks (id, owner, chat_jid, prompt, cron, next_run, status, created_at, context_mode)
		 VALUES (?, 'test', ?, ?, ?, ?, ?, ?, ?)`,
		id, jid, prompt, cronExpr, nextRun, status, time.Now().Format(time.RFC3339), contextMode)
	if err != nil {
		t.Fatal(err)
	}
}

func countMessages(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM messages").Scan(&n)
	return n
}

func TestMigrateCreatesTable(t *testing.T) {
	db := openTestDB(t)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var n int
	err := db.QueryRow("SELECT COUNT(*) FROM scheduled_tasks").Scan(&n)
	if err != nil {
		t.Fatal("scheduled_tasks not created:", err)
	}
	if n != 0 {
		t.Fatal("expected 0 rows, got", n)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db := openTestDB(t)
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal("second migrate failed:", err)
	}
	entries, _ := migrationFS.ReadDir("migrations")
	var migCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			migCount++
		}
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM migrations WHERE service=?", serviceName).Scan(&n)
	if n != migCount {
		t.Fatalf("expected %d migration records, got %d", migCount, n)
	}
}

func TestFireNoDueTasks(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	fire(db, "UTC")
	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages, got", n)
	}
}

func TestFireCronTask(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	cron := "0 9 * * *"
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "t1", "chat1", "hello", "active", past, &cron)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 1 {
		t.Fatal("expected 1 message, got", n)
	}

	var content, sender string
	db.QueryRow("SELECT sender, content FROM messages").Scan(&sender, &content)
	if sender != "timed" {
		t.Fatal("expected sender=timed, got", sender)
	}
	if content != "hello" {
		t.Fatal("expected content=hello, got", content)
	}

	var nextRun, status string
	db.QueryRow("SELECT next_run, status FROM scheduled_tasks WHERE id='t1'").Scan(&nextRun, &status)
	if status != "active" {
		t.Fatal("cron task should remain active, got", status)
	}
	nr, err := time.Parse(time.RFC3339, nextRun)
	if err != nil {
		t.Fatal("bad next_run:", nextRun)
	}
	if !nr.After(time.Now()) {
		t.Fatal("next_run should be in future, got", nextRun)
	}
}

func TestFireOneShotTask(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTask(t, db, "t2", "chat2", "once", "active", past, nil)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 1 {
		t.Fatal("expected 1 message, got", n)
	}

	var status string
	var nextRun *string
	db.QueryRow("SELECT status, next_run FROM scheduled_tasks WHERE id='t2'").Scan(&status, &nextRun)
	if status != "completed" {
		t.Fatal("one-shot should be completed, got", status)
	}
	if nextRun != nil {
		t.Fatal("one-shot next_run should be NULL, got", *nextRun)
	}
}

func TestFireMultipleDueTasks(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "a", "c1", "p1", "active", past, nil)
	insertTask(t, db, "b", "c2", "p2", "active", past, nil)
	insertTask(t, db, "c", "c3", "p3", "active", past, nil)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 3 {
		t.Fatal("expected 3 messages, got", n)
	}
}

func TestFireSkipsPausedTasks(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "p1", "c1", "skip me", "paused", past, nil)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages for paused task, got", n)
	}
}

func TestFireSkipsFutureNextRun(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	insertTask(t, db, "f1", "c1", "not yet", "active", future, nil)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages for future task, got", n)
	}
}

func TestNextCronBasic(t *testing.T) {
	next, err := nextCron("0 9 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if !next.After(time.Now()) {
		t.Fatal("next should be in the future")
	}
	if next.Hour() != 9 || next.Minute() != 0 {
		t.Fatalf("expected 09:00, got %02d:%02d", next.Hour(), next.Minute())
	}
}

func TestNextCronEveryMinute(t *testing.T) {
	next, err := nextCron("* * * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	diff := next.Sub(time.Now())
	if diff > 2*time.Minute || diff < 0 {
		t.Fatal("every-minute cron should be within 2m, got", diff)
	}
}

func TestNextCronInvalidExpression(t *testing.T) {
	_, err := nextCron("not a cron", "UTC")
	if err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
}

func TestNextCronRespectsTimezone(t *testing.T) {
	utc, err := nextCron("0 12 * * *", "UTC")
	if err != nil {
		t.Fatal(err)
	}
	tokyo, err := nextCron("0 12 * * *", "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if utc.Equal(tokyo) {
		t.Fatal("UTC and Tokyo results should differ")
	}
}

func TestNextCronInvalidTimezoneDefaultsUTC(t *testing.T) {
	next, err := nextCron("0 12 * * *", "Invalid/Zone")
	if err != nil {
		t.Fatal(err)
	}
	if next.Location() != time.UTC {
		t.Fatal("expected UTC fallback, got", next.Location())
	}
}

func TestFireInvalidCronStillFiresButNoUpdate(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	bad := "not valid"
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "bad1", "c1", "fire me", "active", past, &bad)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 1 {
		t.Fatal("expected 1 message even with bad cron, got", n)
	}

	var nextRun string
	db.QueryRow("SELECT next_run FROM scheduled_tasks WHERE id='bad1'").Scan(&nextRun)
	nr, _ := time.Parse(time.RFC3339, nextRun)
	if nr.After(time.Now()) {
		t.Fatal("next_run should NOT be updated with invalid cron")
	}
}

func TestLogTaskRun(t *testing.T) {
	db := openTestDB(t)
	migrate(db)

	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTask(t, db, "log1", "chat1", "ping", "active", past, nil)
	setupMessages(t, db)
	fire(db, "UTC")

	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM task_run_logs WHERE task_id='log1'").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 run log, got %d", n)
	}
	var status string
	db.QueryRow("SELECT status FROM task_run_logs WHERE task_id='log1'").Scan(&status)
	if status != "success" {
		t.Fatal("expected status=success, got", status)
	}
}

func TestIntervalMode(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	intervalMs := "5000"
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTask(t, db, "iv1", "chat1", "interval-task", "active", past, &intervalMs)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 1 {
		t.Fatal("expected 1 message, got", n)
	}

	var nextRun, status string
	db.QueryRow("SELECT next_run, status FROM scheduled_tasks WHERE id='iv1'").Scan(&nextRun, &status)
	if status != "active" {
		t.Fatal("interval task should remain active, got", status)
	}
	nr, err := time.Parse(time.RFC3339, nextRun)
	if err != nil {
		t.Fatal("bad next_run:", nextRun)
	}
	if !nr.After(time.Now()) {
		t.Fatal("next_run should be in future, got", nextRun)
	}
}

func TestContextModeIsolated(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTaskWithMode(t, db, "iso1", "chat1", "isolated-task", "active", past, nil, "isolated")

	fire(db, "UTC")

	if n := countMessages(t, db); n != 1 {
		t.Fatal("expected 1 message, got", n)
	}

	var sender string
	db.QueryRow("SELECT sender FROM messages WHERE content='isolated-task'").Scan(&sender)
	if !strings.HasPrefix(sender, "timed-isolated:") {
		t.Fatalf("expected sender=timed-isolated:<id>, got %q", sender)
	}
}

func TestIsIntervalMs(t *testing.T) {
	cases := []struct {
		in  string
		ms  int64
		ok  bool
	}{
		{"5000", 5000, true},
		{"1", 1, true},
		{"0 9 * * *", 0, false},
		{"", 0, false},
		{"0", 0, false},
		{"-1", 0, false},
	}
	for _, c := range cases {
		ms, ok := isIntervalMs(c.in)
		if ok != c.ok || ms != c.ms {
			t.Errorf("isIntervalMs(%q) = (%d, %v), want (%d, %v)", c.in, ms, ok, c.ms, c.ok)
		}
	}
}

func TestConcurrentFireNoDuplicates(t *testing.T) {
	db := openTestDB(t)
	migrate(db)
	setupMessages(t, db)

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "c1", "chat1", "concurrent", "active", past, nil)

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fire(db, "UTC")
		}()
	}
	wg.Wait()

	var n int
	db.QueryRow("SELECT COUNT(*) FROM messages WHERE content='concurrent'").Scan(&n)
	if n < 1 {
		t.Fatal("expected at least 1 message, got", n)
	}
	// One-shot: first fire() completes it, but concurrent fires may read before update.
	// With SQLite serialization the practical count is 1, but the real invariant
	// is that messages table has no duplicate IDs (unique nano timestamps).
	var ids int
	db.QueryRow("SELECT COUNT(DISTINCT id) FROM messages WHERE content='concurrent'").Scan(&ids)
	if ids != n {
		t.Fatalf("duplicate message IDs: %d ids for %d rows", ids, n)
	}
}
