package main

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := openDB(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenDBFreshMigrates(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{"email_threads", "email_msg_ids"} {
		var name string
		err := db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`,
			table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	var version int
	err := db.QueryRow(
		`SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?`,
		serviceName).Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("migration version = %d, want 1", version)
	}
}

// TestOpenDBAdoptsPreMigrationFile protects the live instances: their
// emaid.db was created by the retired inline CREATE TABLE, so it holds
// both tables and rows but no migrations table. openDB must open that
// file, keep its rows, and record migration version 1.
func TestOpenDBAdoptsPreMigrationFile(t *testing.T) {
	dir := t.TempDir()
	old, err := sql.Open("sqlite", filepath.Join(dir, "emaid.db"))
	if err != nil {
		t.Fatal(err)
	}
	// the retired inline schema, verbatim
	_, err = old.Exec(`
		CREATE TABLE IF NOT EXISTS email_threads (
			thread_id TEXT PRIMARY KEY,
			from_address TEXT NOT NULL,
			root_msg_id TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS email_msg_ids (
			msg_id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	upsertThread(old, "msg-1@x.com", "tid1", "alice@x.com", "msg-1@x.com")
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := openDB(dir)
	if err != nil {
		t.Fatalf("openDB on a pre-migration file: %v", err)
	}
	defer db.Close()
	got := getThreadByMsgID(db, "msg-1@x.com")
	if got == nil {
		t.Fatal("pre-existing row lost after migration")
	}
	if got.ThreadID != "tid1" || got.FromAddress != "alice@x.com" {
		t.Fatalf("pre-existing row changed: %+v", got)
	}
	var version int
	err = db.QueryRow(
		`SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?`,
		serviceName).Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	if version != 1 {
		t.Fatalf("migration version = %d, want 1", version)
	}
}

func TestThreadStore(t *testing.T) {
	db := newTestDB(t)
	upsertThread(db, "msg-1@x.com", "aabbcc112233", "alice@x.com", "msg-1@x.com")

	got := getThreadByMsgID(db, "msg-1@x.com")
	if got == nil {
		t.Fatal("expected thread, got nil")
	}
	if got.ThreadID != "aabbcc112233" {
		t.Errorf("thread_id = %q", got.ThreadID)
	}
	if got.FromAddress != "alice@x.com" {
		t.Errorf("from_address = %q", got.FromAddress)
	}
	if got.RootMsgID != "msg-1@x.com" {
		t.Errorf("root_msg_id = %q", got.RootMsgID)
	}
}

func TestDedupByMsgID(t *testing.T) {
	db := newTestDB(t)
	upsertThread(db, "msg-1@x.com", "tid1", "alice@x.com", "msg-1@x.com")
	// second call with same msgID must be a no-op (INSERT OR IGNORE)
	upsertThread(db, "msg-1@x.com", "tid2", "bob@x.com", "msg-1@x.com")

	got := getThreadByMsgID(db, "msg-1@x.com")
	if got == nil {
		t.Fatal("expected thread")
	}
	if got.ThreadID != "tid1" {
		t.Errorf("dedup failed: thread_id = %q, want tid1", got.ThreadID)
	}
}

func TestInReplyToChain(t *testing.T) {
	db := newTestDB(t)
	upsertThread(db, "root@x.com", "rootTID", "alice@x.com", "root@x.com")
	// reply links to root via msg_ids
	upsertThread(db, "reply@x.com", "rootTID", "alice@x.com", "root@x.com")

	got := getThreadByMsgID(db, "reply@x.com")
	if got == nil {
		t.Fatal("expected thread for reply")
	}
	if got.ThreadID != "rootTID" {
		t.Errorf("thread_id = %q, want rootTID", got.ThreadID)
	}
	if got.RootMsgID != "root@x.com" {
		t.Errorf("root_msg_id = %q, want root@x.com", got.RootMsgID)
	}
}

func TestConcurrentInsert(t *testing.T) {
	// INSERT OR IGNORE: second insert with same msgID must not overwrite the first.
	// Tests idempotency (real concurrency is serialized at the DB driver level).
	db := newTestDB(t)
	var wg sync.WaitGroup
	errs := make(chan struct{}, 2)
	for range 2 {
		wg.Go(func() {
			defer func() {
				if r := recover(); r != nil {
					errs <- struct{}{}
				}
			}()
			upsertThread(db, "msg-dup@x.com", "tidX", "a@x.com", "msg-dup@x.com")
		})
	}
	wg.Wait()
	close(errs)

	if len(errs) > 0 {
		t.Fatal("storeThread panicked under concurrent access")
	}
	got := getThreadByMsgID(db, "msg-dup@x.com")
	if got == nil {
		t.Fatal("expected thread after concurrent insert")
	}
}

// A failed mapping write must REACH the caller. Losing it silently costs the
// user a split conversation: with no email_msg_ids row, the next reply misses
// in getThreadByMsgID and opens a second thread (BUGS L3).
func TestUpsertThreadSurfacesWriteFailure(t *testing.T) {
	db := newTestDB(t)
	if err := upsertThread(db, "msg-1@x.com", "tid1", "alice@x.com", "msg-1@x.com"); err != nil {
		t.Fatalf("healthy write returned %v, want nil", err)
	}

	// Drop the table the second INSERT needs: the write now fails mid-transaction.
	if _, err := db.Exec(`DROP TABLE email_msg_ids`); err != nil {
		t.Fatal(err)
	}
	err := upsertThread(db, "msg-2@x.com", "tid2", "bob@x.com", "msg-2@x.com")
	if err == nil {
		t.Fatal("a dropped mapping returned nil: the caller would deliver the " +
			"message as if the thread had been recorded")
	}
	if !strings.Contains(err.Error(), "msg-2@x.com") {
		t.Errorf("error %q does not name the message that was lost", err)
	}
}
