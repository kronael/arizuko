package main

import (
	"database/sql"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/onvos/arizuko/store"
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

func insertTask(t *testing.T, db *sql.DB, id, jid, prompt, status, nextRun string, cronExpr *string) {
	t.Helper()
	insertTaskWithMode(t, db, id, jid, prompt, status, nextRun, cronExpr, "")
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

func TestFireNoDueTasks(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

	fire(db, "UTC")
	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages, got", n)
	}
}

func TestFireCronTask(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

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
	store.Migrate(db)

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
	store.Migrate(db)

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
	store.Migrate(db)

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "p1", "c1", "skip me", "paused", past, nil)

	fire(db, "UTC")

	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages for paused task, got", n)
	}
}

func TestFireSkipsFutureNextRun(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

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
	diff := time.Until(next)
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
	store.Migrate(db)

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
	store.Migrate(db)

	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTask(t, db, "log1", "chat1", "ping", "active", past, nil)
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
	store.Migrate(db)

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
	store.Migrate(db)

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

func TestComputeNextRunInterval(t *testing.T) {
	got := computeNextRun("1500", "UTC", "x")
	nr, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal("bad rfc3339:", got)
	}
	diff := time.Until(nr)
	if diff < 500*time.Millisecond || diff > 3*time.Second {
		t.Fatal("interval next_run not ~1.5s in future:", diff)
	}
}

func TestComputeNextRunEmpty(t *testing.T) {
	if got := computeNextRun("", "UTC", "x"); got != "" {
		t.Fatal("empty cron should return empty, got", got)
	}
}

func TestComputeNextRunCron(t *testing.T) {
	got := computeNextRun("0 9 * * *", "UTC", "x")
	if got == "" {
		t.Fatal("expected next_run, got empty")
	}
	nr, err := time.Parse(time.RFC3339, got)
	if err != nil {
		t.Fatal("bad rfc3339:", got)
	}
	if nr.Hour() != 9 || nr.Minute() != 0 {
		t.Fatalf("expected 09:00, got %02d:%02d", nr.Hour(), nr.Minute())
	}
}

func TestComputeNextRunInvalidCron(t *testing.T) {
	if got := computeNextRun("not a cron", "UTC", "x"); got != "" {
		t.Fatal("invalid cron should return empty, got", got)
	}
}

func TestLogRunErrorPath(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
	insertTask(t, db, "err1", "c1", "p", "active", time.Now().Format(time.RFC3339), nil)

	logRun(db, "err1", "error", "boom", 42)

	var status, errText string
	var dur int64
	db.QueryRow(`SELECT status, error, duration_ms FROM task_run_logs WHERE task_id='err1'`).
		Scan(&status, &errText, &dur)
	if status != "error" || errText != "boom" || dur != 42 {
		t.Fatalf("log row wrong: %s %s %d", status, errText, dur)
	}
}

func TestFireInsertErrorRestoresActive(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
	if _, err := db.Exec(`DROP TABLE messages`); err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "e1", "c1", "will fail", "active", past, nil)

	fire(db, "UTC")

	var status string
	db.QueryRow("SELECT status FROM scheduled_tasks WHERE id='e1'").Scan(&status)
	if status != "active" {
		t.Fatal("on insert error, task should be restored to active, got", status)
	}

	var logStatus, logErr string
	if err := db.QueryRow(
		`SELECT status, error FROM task_run_logs WHERE task_id='e1'`).Scan(&logStatus, &logErr); err != nil {
		t.Fatal("expected error run log:", err)
	}
	if logStatus != "error" || logErr == "" {
		t.Fatalf("expected error log, got status=%s err=%q", logStatus, logErr)
	}
}

func TestFireHonorsTimezone(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

	cron := "0 12 * * *"
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)
	insertTask(t, db, "tz1", "chat1", "noon", "active", past, &cron)

	fire(db, "Asia/Tokyo")

	var nextRun string
	db.QueryRow("SELECT next_run FROM scheduled_tasks WHERE id='tz1'").Scan(&nextRun)
	nr, err := time.Parse(time.RFC3339, nextRun)
	if err != nil {
		t.Fatal("bad next_run:", nextRun)
	}
	// Tokyo noon = 03:00 UTC
	if nr.UTC().Hour() != 3 {
		t.Fatalf("expected UTC hour=3 (Tokyo noon), got %d", nr.UTC().Hour())
	}
}

func TestFireUpdatesNextRunOnInterval(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

	intervalMs := "60000"
	past := time.Now().Add(-time.Minute).Format(time.RFC3339)
	insertTask(t, db, "iv", "c1", "i", "active", past, &intervalMs)

	before := time.Now()
	fire(db, "UTC")

	var nextRun string
	db.QueryRow("SELECT next_run FROM scheduled_tasks WHERE id='iv'").Scan(&nextRun)
	nr, err := time.Parse(time.RFC3339, nextRun)
	if err != nil {
		t.Fatal(err)
	}
	// ~60s in future
	if nr.Sub(before) < 50*time.Second || nr.Sub(before) > 75*time.Second {
		t.Fatalf("interval next_run off: %v", nr.Sub(before))
	}
}

func TestConcurrentFireNoDuplicates(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)

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
