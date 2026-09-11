package main

// The one-way move of an emaid.db, on a database built to the shape a live
// instance would carry.
//
// No instance runs emaid, so there is no snapshot to rehearse against and the
// schema below is the one emaid created — the migration's text and the inline
// CREATE TABLE that preceded it were byte-identical, so one body covers both
// kinds of file on disk.

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// emaidLegacySchema is the two tables emaid used to own.
const emaidLegacySchema = `
CREATE TABLE email_threads (
  thread_id    TEXT PRIMARY KEY,
  from_address TEXT NOT NULL,
  root_msg_id  TEXT NOT NULL);
CREATE TABLE email_msg_ids (
  msg_id    TEXT PRIMARY KEY,
  thread_id TEXT NOT NULL);`

// seedEmaidLegacy writes a conversation of two mails plus an index row naming a
// thread that is not there — the state the old schema allowed, because it
// declared no foreign key.
func seedEmaidLegacy(t *testing.T, path string) {
	t.Helper()
	db, err := db_utils.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(
		`INSERT INTO email_threads (thread_id, from_address, root_msg_id)
		 VALUES ('aabbcc112233', 'alice@x.com', 'root@x.com')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range [][2]string{
		{"root@x.com", "aabbcc112233"},
		{"reply@x.com", "aabbcc112233"},
		{"orphan@x.com", "gone"},
	} {
		if _, err := db.Exec(
			`INSERT INTO email_msg_ids (msg_id, thread_id) VALUES (?, ?)`, row[0], row[1]); err != nil {
			t.Fatal(err)
		}
	}
}

// The move writes the entities, sets the old tables aside under _v1, and the
// rows read back through the methods emaid itself calls.
func TestMigrateEmaidMovesTheRowsAndSetsTheTablesAside(t *testing.T) {
	path, db := legacyDB(t, "emaid.db", emaidLegacySchema)
	seedEmaidLegacy(t, path)

	if err := migrateEmaid(path, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := emaistore.New(sqlstore.New(db))
	got, ok := st.ThreadForMessage("reply@x.com")
	if !ok {
		t.Fatal("a migrated reply resolves to no thread")
	}
	want := emaistore.Thread{ThreadID: "aabbcc112233", FromAddress: "alice@x.com", RootMsgID: "root@x.com"}
	if got != want {
		t.Fatalf("the reply resolves to %+v, want %+v", got, want)
	}
	// A dangling index row carries over and reads as a miss, which is what the
	// old inner join answered for it too.
	if _, ok := st.ThreadForMessage("orphan@x.com"); ok {
		t.Error("an index row naming a thread nobody wrote resolved to one")
	}

	// The evidence stays on disk under its new name, with its rows.
	var threads int
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_threads_v1`).Scan(&threads); err != nil {
		t.Fatalf("the old table was dropped rather than set aside: %v", err)
	}
	if threads != 1 {
		t.Errorf("email_threads_v1 holds %d rows, want 1", threads)
	}
	var msgs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM email_msg_ids_v1`).Scan(&msgs); err != nil {
		t.Fatalf("the old index was dropped rather than set aside: %v", err)
	}
	if msgs != 3 {
		t.Errorf("email_msg_ids_v1 holds %d rows, want 3", msgs)
	}
}

// A dry run reads and checks the source and writes nothing at all: the tables
// keep their names and no entity appears.
func TestMigrateEmaidDryRunWritesNothing(t *testing.T) {
	path, db := legacyDB(t, "emaid.db", emaidLegacySchema)
	seedEmaidLegacy(t, path)

	if err := migrateEmaid(path, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='email_threads_v1'`).Scan(&name); err == nil {
		t.Error("a dry run set the old table aside")
	}
	// The three-column entity is not there, so the table still holds thread_id.
	legacy, err := isLegacyShape(db, "email_threads")
	if err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Error("a dry run replaced the table with an entity")
	}
}

// An entity keeps the name of the table it replaces, so presence alone cannot
// tell a migrated database from an unmigrated one. Running the tool on a
// database that holds only entities must report that there is nothing to do —
// not read the three-column entity as if it carried `thread_id`.
func TestMigrateEmaidLeavesAnAlreadyMigratedDatabaseAlone(t *testing.T) {
	path, db := legacyDB(t, "emaid.db", "")
	s := sqlstore.New(db)
	for _, entity := range emaistore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	st := emaistore.New(s)
	if err := st.LinkMessageToThread("root@x.com", "tid", "alice@x.com", "root@x.com"); err != nil {
		t.Fatal(err)
	}
	if err := migrateEmaid(path, false); err != nil {
		t.Fatalf("a database that holds only entities must be left alone: %v", err)
	}
	// Anti-vacuity: the conversation that was there is still there, so "nothing
	// to migrate" did not mean "nothing was read".
	if got, ok := st.ThreadForMessage("root@x.com"); !ok || got.FromAddress != "alice@x.com" {
		t.Fatalf("the existing thread came back as %+v (found=%v)", got, ok)
	}
}

// A row the entities cannot address stops the run, by name, and writes nothing.
// An id IS the partition, and a query without one is refused by every driver,
// so a blank thread_id would write an item nothing could ever read back.
func TestMigrateEmaidRefusesAnUnaddressableRow(t *testing.T) {
	path, db := legacyDB(t, "emaid.db", emaidLegacySchema)
	if _, err := db.Exec(
		`INSERT INTO email_threads (thread_id, from_address, root_msg_id)
		 VALUES ('', 'alice@x.com', 'root@x.com')`); err != nil {
		t.Fatal(err)
	}
	err := migrateEmaid(path, false)
	if err == nil {
		t.Fatal("a thread with no id was accepted")
	}
	if !strings.Contains(err.Error(), "no thread_id") {
		t.Errorf("the refusal %q does not name what is wrong", err)
	}
	if _, qerr := db.Query(`SELECT 1 FROM email_threads_v1`); qerr == nil {
		t.Error("the run set the old table aside before it refused")
	}
}
