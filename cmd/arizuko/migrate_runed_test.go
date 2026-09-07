package main

// The one-way move of a runed.db, on a database built to the shape the three
// live instances carry: RFC3339 at second resolution in every timestamp column,
// an AUTOINCREMENT session id, and sessions sharing a second by the dozen.

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/runed/runstore"
)

// runedLegacySchema is the three tables runed used to own, as migration 0001,
// 0002 and 0004 left them.
const runedLegacySchema = `
CREATE TABLE session_log (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  group_folder  TEXT NOT NULL,
  session_id    TEXT NOT NULL,
  started_at    TEXT NOT NULL,
  ended_at      TEXT,
  result        TEXT,
  error         TEXT,
  message_count INTEGER);
CREATE TABLE spawns (
  run_id         TEXT PRIMARY KEY,
  folder         TEXT NOT NULL,
  topic          TEXT NOT NULL DEFAULT '',
  container_name TEXT NOT NULL,
  session_log_id INTEGER REFERENCES session_log(id),
  session_id     TEXT,
  state          TEXT NOT NULL,
  outcome        TEXT,
  exit_code      INTEGER,
  steered        INTEGER NOT NULL DEFAULT 0,
  created_at     TEXT NOT NULL,
  started_at     TEXT,
  ended_at       TEXT,
  kind           TEXT NOT NULL DEFAULT 'agent');
CREATE TABLE circuit_breaker (
  folder        TEXT PRIMARY KEY,
  failures      INTEGER NOT NULL DEFAULT 0,
  last_failure  TEXT);`

// seedRunedLegacy writes three sessions that share one second — the state krons
// carries 760 times over — plus the spawn that opened the last of them and a
// folder mid-streak.
func seedRunedLegacy(t *testing.T, path string) {
	t.Helper()
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, sess := range []string{"first", "second", "third"} {
		if _, err := db.Exec(
			`INSERT INTO session_log (group_folder, session_id, started_at, ended_at,
			 result, error, message_count) VALUES ('acme', ?, '2026-09-05T20:19:00Z',
			 '2026-09-05T20:20:00Z', 'ok', '', 4)`, sess); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(
		`INSERT INTO spawns (run_id, folder, topic, container_name, session_log_id,
		 session_id, state, outcome, exit_code, steered, created_at, started_at,
		 ended_at, kind)
		 VALUES ('run_abc', 'acme', 'why', 'arizuko-x-acme-1', 3, 'third', 'exited',
		 'ok', 0, 1, '2026-09-05T20:19:00Z', '2026-09-05T20:19:01Z',
		 '2026-09-05T20:20:00Z', 'agent')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO circuit_breaker (folder, failures, last_failure)
		 VALUES ('broken', 2, '2026-09-05T20:19:00Z')`); err != nil {
		t.Fatal(err)
	}
}

// The move writes the entities, sets the old tables aside under _v1, and every
// row reads back through the methods runed itself calls — in the order the
// sequence had, which three sessions inside one second is the whole test of.
func TestMigrateRunedMovesTheRowsAndSetsTheTablesAside(t *testing.T) {
	path, db := legacyDB(t, "runed.db", runedLegacySchema)
	seedRunedLegacy(t, path)

	if err := migrateRuned(path, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := runstore.New(sqlstore.New(db))
	rows, err := st.RecentSessions(t.Context(), "acme", 10)
	if err != nil {
		t.Fatalf("read the sessions back: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("read back %d sessions, want 3", len(rows))
	}
	for i, want := range []string{"third", "second", "first"} {
		if rows[i].SessionID != want {
			t.Fatalf("session %d reads back %q, want %q — the order the sequence had "+
				"was not carried through a shared second", i, rows[i].SessionID, want)
		}
	}
	if rows[0].Result != "ok" || rows[0].MessageCount != 4 || rows[0].EndedAt == "" {
		t.Errorf("the newest session reads back %+v", rows[0])
	}

	sp, err := st.SpawnByRunID(t.Context(), "run_abc")
	if err != nil {
		t.Fatalf("read the run back: %v", err)
	}
	if sp.Folder != "acme" || sp.Topic != "why" || sp.State != "exited" ||
		sp.Outcome != "ok" || sp.Kind != "agent" || !sp.Steered {
		t.Fatalf("the run reads back %+v", sp)
	}
	// The link resolved through the sessions the same snapshot carried.
	if sp.SessionLogID != rows[0].ID {
		t.Errorf("the run links to %q and its session is %q", sp.SessionLogID, rows[0].ID)
	}
	// Every timestamp is core.TS's width now, which is what lets the sweep
	// compare created_at by byte order (BUGS L97).
	if len(sp.CreatedAt) != 30 {
		t.Errorf("created_at came over as %q, %d bytes", sp.CreatedAt, len(sp.CreatedAt))
	}
	if n, err := st.FailureStreak(t.Context(), "broken"); err != nil || n != 2 {
		t.Errorf("the failure streak reads back (%d, %v), want (2, nil)", n, err)
	}

	// The evidence stays on disk under its new name, with its rows.
	for table, want := range map[string]int{
		"session_log_v1": 3, "spawns_v1": 1, "circuit_breaker_v1": 1,
	} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("%s was dropped rather than set aside: %v", table, err)
		}
		if n != want {
			t.Errorf("%s holds %d rows, want %d", table, n, want)
		}
	}
}

// A dry run reads and checks the source and writes nothing at all.
func TestMigrateRunedDryRunWritesNothing(t *testing.T) {
	path, db := legacyDB(t, "runed.db", runedLegacySchema)
	seedRunedLegacy(t, path)

	if err := migrateRuned(path, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='spawns_v1'`).Scan(&name); err == nil {
		t.Error("a dry run set the old table aside")
	}
	legacy, err := isLegacyShape(db, "spawns")
	if err != nil {
		t.Fatal(err)
	}
	if !legacy {
		t.Error("a dry run replaced the table with an entity")
	}
}

// An entity keeps the name of the table it replaces, so presence alone cannot
// tell a migrated database from an unmigrated one. Running the tool on a
// database that holds only entities must report there is nothing to do — not
// read the three-column entity as if it carried `run_id`.
func TestMigrateRunedLeavesAnAlreadyMigratedDatabaseAlone(t *testing.T) {
	path, db := legacyDB(t, "runed.db", "")
	s := sqlstore.New(db)
	if err := runstore.EnsureEntities(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	st := runstore.New(s)
	if err := st.CreateSpawn(t.Context(), runstore.Spawn{
		RunID: "run_1", Folder: "acme", ContainerName: "c", State: "queued",
	}); err != nil {
		t.Fatal(err)
	}
	if err := migrateRuned(path, false); err != nil {
		t.Fatalf("a database that holds only entities must be left alone: %v", err)
	}
	// Anti-vacuity: the run that was there is still there, so "nothing to
	// migrate" did not mean "nothing was read".
	if sp, err := st.SpawnByRunID(t.Context(), "run_1"); err != nil || sp.Folder != "acme" {
		t.Fatalf("the existing run came back as %+v (err=%v)", sp, err)
	}
}

// A row the entities cannot address stops the run, by name, and writes nothing.
// The folder IS the session partition, and a query without one is refused by
// every driver, so a blank group_folder would write an item nothing reads back.
func TestMigrateRunedRefusesAnUnaddressableRow(t *testing.T) {
	path, db := legacyDB(t, "runed.db", runedLegacySchema)
	if _, err := db.Exec(
		`INSERT INTO session_log (group_folder, session_id, started_at)
		 VALUES ('', 's', '2026-09-05T20:19:00Z')`); err != nil {
		t.Fatal(err)
	}
	err := migrateRuned(path, false)
	if err == nil {
		t.Fatal("a session with no folder was accepted")
	}
	if !strings.Contains(err.Error(), "names no group_folder") {
		t.Errorf("the refusal %q does not name what is wrong", err)
	}
	if _, qerr := db.Query(`SELECT 1 FROM session_log_v1`); qerr == nil {
		t.Error("the run set the old table aside before it refused")
	}
}
