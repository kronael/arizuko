package main

import (
	"database/sql"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

func newTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
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
	t.Cleanup(func() { db.Close() })
	return db
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
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					errs <- struct{}{}
				}
			}()
			upsertThread(db, "msg-dup@x.com", "tidX", "a@x.com", "msg-dup@x.com")
		}()
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
