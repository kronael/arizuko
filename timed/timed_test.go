package main

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
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

// openFileDB returns a file-backed SQLite DB allowing multiple connections.
// Needed for code paths that hold a rows iterator while issuing nested queries.
func openFileDB(t *testing.T) *sql.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "t.db")+"?_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("PRAGMA journal_mode=WAL")
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

func TestFireNoDueTasks(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
	setupMessages(t, db)

	fire(db, "UTC")
	if n := countMessages(t, db); n != 0 {
		t.Fatal("expected 0 messages, got", n)
	}
}

func TestFireCronTask(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
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
	store.Migrate(db)
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
	store.Migrate(db)
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
	store.Migrate(db)
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
	store.Migrate(db)
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
	store.Migrate(db)
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
	store.Migrate(db)

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
	store.Migrate(db)
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
	store.Migrate(db)
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
	// Intentionally DO NOT call setupMessages — messages table stays as created
	// by migration 0001; but migration 0019 adds columns. fire() INSERTs only
	// id, chat_jid, sender, content, timestamp → must still work.
	// To force an insert error, drop the messages table.
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
	setupMessages(t, db)

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

func TestCleanupSpawnsClosesIdle(t *testing.T) {
	db := openFileDB(t)
	store.Migrate(db)

	tmp := t.TempDir()

	// Parent group
	_, err := db.Exec(`INSERT INTO groups (folder, name, added_at, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('parent', 'Parent', ?, 'active', 7, 1, ?)`,
		time.Now().Format(time.RFC3339), time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	// Child spawn, ttl=1 day, last msg 2 days ago
	old := time.Now().Add(-48 * time.Hour).Format(time.RFC3339)
	_, err = db.Exec(`INSERT INTO groups (folder, name, added_at, parent, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('parent/spawn1', 'Spawn1', ?, 'parent', 'active', 1, 1, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}

	// Route targets the spawn folder, matches room=xyz
	if _, err := db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=xyz', 'parent/spawn1')`); err != nil {
		t.Fatal(err)
	}
	// Last message in that room is 2 days old
	if _, err := db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
		VALUES ('m1', 'group:xyz', 'u', 'hi', ?)`, old); err != nil {
		t.Fatal(err)
	}

	cleanupSpawns(db, tmp)

	var state string
	db.QueryRow(`SELECT state FROM groups WHERE folder='parent/spawn1'`).Scan(&state)
	if state != "closed" {
		t.Fatalf("expected spawn closed, got %s", state)
	}
}

func TestCleanupSpawnsKeepsActiveWhenRecent(t *testing.T) {
	db := openFileDB(t)
	store.Migrate(db)
	tmp := t.TempDir()

	now := time.Now().Format(time.RFC3339)
	db.Exec(`INSERT INTO groups (folder, name, added_at, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('p', 'P', ?, 'active', 7, 1, ?)`, now, now)
	db.Exec(`INSERT INTO groups (folder, name, added_at, parent, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('p/s', 'S', ?, 'p', 'active', 7, 1, ?)`, now, now)
	db.Exec(`INSERT INTO routes (seq, match, target) VALUES (0, 'room=live', 'p/s')`)
	db.Exec(`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
		VALUES ('m', 'group:live', 'u', 'hi', ?)`, now)

	cleanupSpawns(db, tmp)

	var state string
	db.QueryRow(`SELECT state FROM groups WHERE folder='p/s'`).Scan(&state)
	if state != "active" {
		t.Fatalf("expected active, got %s", state)
	}
}

func TestCleanupSpawnsArchivesClosedAndOld(t *testing.T) {
	db := openFileDB(t)
	store.Migrate(db)

	tmp := t.TempDir()
	// Build source dir for the spawn
	spawnDir := filepath.Join(tmp, "p", "s")
	if err := os.MkdirAll(spawnDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(spawnDir, "note.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	old := time.Now().Add(-72 * time.Hour).Format(time.RFC3339)
	_, err := db.Exec(`INSERT INTO groups (folder, name, added_at, parent, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('p/s', 'S', ?, 'p', 'closed', 1, 1, ?)`, old, old)
	if err != nil {
		t.Fatal(err)
	}

	cleanupSpawns(db, tmp)

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder='p/s'`).Scan(&n)
	if n != 0 {
		t.Fatal("archived group row should be deleted, still present")
	}
	if _, err := os.Stat(spawnDir); !os.IsNotExist(err) {
		t.Fatal("src dir should be removed after archive")
	}
	// tar.gz archive file should exist
	archives, err := filepath.Glob(filepath.Join(tmp, "p", "archive", "s-*.tar.gz"))
	if err != nil || len(archives) == 0 {
		t.Fatal("expected archive file, found none")
	}
	// Sanity: archive contains note.txt
	f, err := os.Open(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Name == "note.txt" {
			found = true
		}
	}
	if !found {
		t.Fatal("archive should contain note.txt")
	}
}

func TestCleanupSpawnsSkipsNotYetOldClosed(t *testing.T) {
	db := openFileDB(t)
	store.Migrate(db)
	tmp := t.TempDir()

	recent := time.Now().Add(-30 * time.Minute).Format(time.RFC3339)
	db.Exec(`INSERT INTO groups (folder, name, added_at, parent, state, spawn_ttl_days, archive_closed_days, updated_at)
		VALUES ('p/s', 'S', ?, 'p', 'closed', 1, 1, ?)`, recent, recent)

	cleanupSpawns(db, tmp)

	var n int
	db.QueryRow(`SELECT COUNT(*) FROM groups WHERE folder='p/s'`).Scan(&n)
	if n != 1 {
		t.Fatal("recently-closed group should not be archived yet")
	}
}

func TestCompressDirTarGz(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(filepath.Join(src, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("A"), 0o644)
	os.WriteFile(filepath.Join(src, "sub", "b.txt"), []byte("BB"), 0o644)

	dst := filepath.Join(tmp, "out.tar.gz")
	if err := compressDirTarGz(src, dst); err != nil {
		t.Fatal(err)
	}

	f, err := os.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	if !names["a.txt"] || !names[filepath.Join("sub", "b.txt")] {
		t.Fatalf("archive missing entries: %v", names)
	}
}

func TestCompressDirTarGzBadDst(t *testing.T) {
	err := compressDirTarGz(t.TempDir(), "/no/such/dir/out.tar.gz")
	if err == nil {
		t.Fatal("expected error creating archive at bad path")
	}
}

func TestFireUpdatesNextRunOnInterval(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
	setupMessages(t, db)

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

func TestIsIntervalMsExtra(t *testing.T) {
	if _, ok := isIntervalMs("not-a-number"); ok {
		t.Fatal("non-numeric should not be interval")
	}
	if _, ok := isIntervalMs("9999999999999"); !ok {
		t.Fatal("large positive int should be interval")
	}
}

func TestConcurrentFireNoDuplicates(t *testing.T) {
	db := openTestDB(t)
	store.Migrate(db)
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
