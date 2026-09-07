package main

// The one-way move of an adapter database, on databases built from the
// migration text chanlib used to ship.
//
// The seed is measured rather than invented. krons carries two live teled
// databases and their `created_at` values are 28, 29 and 30 bytes across 110
// rows — written before core.TS was the layout — so the fixture carries all
// three widths. Nothing reads that column, which is why chanstore declares no
// width for it, and this is the body that proves a declaration there would have
// refused rows the old schema was happy to hold.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// chanlibLegacySchema is the two tables chanlib used to own, with the indexes
// its migration text created.
const chanlibLegacySchema = `
CREATE TABLE outbound_dedup (
  idempotency_key TEXT PRIMARY KEY,
  chat_jid        TEXT NOT NULL,
  state           TEXT NOT NULL,
  platform_id     TEXT,
  created_at      TEXT NOT NULL,
  settled_at      TEXT
);
CREATE INDEX outbound_dedup_created ON outbound_dedup(created_at);
CREATE TABLE inbound_pending (
  id              TEXT PRIMARY KEY,
  payload         TEXT NOT NULL,
  attempts        INTEGER NOT NULL DEFAULT 0,
  first_seen_at   TEXT NOT NULL,
  next_attempt_at TEXT NOT NULL,
  last_error      TEXT
);
CREATE INDEX inbound_pending_due ON inbound_pending(next_attempt_at);`

// seedChanlibLegacy writes what a live adapter database holds: settled claims
// carrying the three timestamp widths krons measured, one claim whose platform
// call never settled, and one inbound message routd has not taken.
func seedChanlibLegacy(t *testing.T, path string) {
	t.Helper()
	db, err := db_utils.Open(db_utils.FileDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, row := range [][]any{
		{"out-f396d61d90c18756", "telegram:user/1112184352", "sent", "6287",
			"2026-08-16T18:44:52.33947691Z", "2026-08-16T18:44:52.464420025Z"},
		{"out-e88ae3f547c96fab", "telegram:user/1112184352", "sent", "6289",
			"2026-08-16T19:05:00.3661786Z", "2026-08-16T19:05:00.498573809Z"},
		{"mcp-6048982074c344ff", "telegram:group/5229089286", "sent", "3069",
			"2026-08-21T12:11:31.369842830Z", "2026-08-21T12:11:31.518631282Z"},
		{"out-never-settled", "telegram:user/1112184352", "inflight", nil,
			"2026-08-21T12:12:00.000000000Z", nil},
	} {
		if _, err := db.Exec(
			`INSERT INTO outbound_dedup
			   (idempotency_key, chat_jid, state, platform_id, created_at, settled_at)
			 VALUES (?,?,?,?,?,?)`, row...); err != nil {
			t.Fatal(err)
		}
	}
	seen := core.TS(fixedSeen)
	if _, err := db.Exec(
		`INSERT INTO inbound_pending (id, payload, attempts, first_seen_at, next_attempt_at, last_error)
		 VALUES (?,?,?,?,?,?)`,
		"slack:c/1/17", `{"id":"slack:c/1/17","chat_jid":"slack:c/1"}`, 3, seen, seen,
		"routd said 503"); err != nil {
		t.Fatal(err)
	}
}

// fixedSeen is when the held message arrived, so a body's reads are not racing
// the clock.
var fixedSeen = time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)

// The move writes the entities, sets the old tables aside under _v1, and every
// row reads back through the methods chanlib itself calls.
func TestMigrateChanlibMovesTheRowsAndSetsTheTablesAside(t *testing.T) {
	path, db := legacyDB(t, "teled.db", chanlibLegacySchema)
	seedChanlibLegacy(t, path)

	if err := migrateChanlib(path, false); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st := chanstore.New(sqlstore.New(db))
	// The claim that decides whether a re-sent message is posted twice.
	got, err := st.ClaimDelivery(t.Context(), "out-f396d61d90c18756", "telegram:user/1112184352")
	if err != nil {
		t.Fatal(err)
	}
	if got.State != chanstore.ClaimSent || got.PlatformID != "6287" {
		t.Fatalf("a migrated settled claim reads as (%v, %q), want (ClaimSent, 6287)",
			got.State, got.PlatformID)
	}
	unsettled, err := st.ClaimDelivery(t.Context(), "out-never-settled", "telegram:user/1112184352")
	if err != nil || unsettled.State != chanstore.ClaimInflight {
		t.Fatalf("a migrated unsettled claim reads as (%v, %v), want ClaimInflight",
			unsettled.State, err)
	}
	// The held message, through the sweep's own read.
	due, err := st.DueForRedelivery(t.Context(), core.TS(fixedSeen.Add(time.Hour)), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != "slack:c/1/17" || due[0].Attempts != 3 {
		t.Fatalf("the sweep reads %+v, want the one held message on attempt 3", due)
	}

	// The evidence stays on disk under its new name, with its rows.
	var claims int
	if err := db.QueryRow(`SELECT COUNT(*) FROM outbound_dedup_v1`).Scan(&claims); err != nil {
		t.Fatalf("the old ledger was dropped rather than set aside: %v", err)
	}
	if claims != 4 {
		t.Errorf("outbound_dedup_v1 holds %d rows, want 4", claims)
	}
	var pending int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbound_pending_v1`).Scan(&pending); err != nil {
		t.Fatalf("the old queue was dropped rather than set aside: %v", err)
	}
	if pending != 1 {
		t.Errorf("inbound_pending_v1 holds %d rows, want 1", pending)
	}
}

// A dry run reads and checks the sources and writes nothing at all: the tables
// keep their names and no entity appears.
func TestMigrateChanlibDryRunWritesNothing(t *testing.T) {
	path, db := legacyDB(t, "teled.db", chanlibLegacySchema)
	seedChanlibLegacy(t, path)

	if err := migrateChanlib(path, true); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	var name string
	if err := db.QueryRow(
		`SELECT name FROM sqlite_master WHERE type='table' AND name='outbound_dedup_v1'`).Scan(&name); err == nil {
		t.Error("a dry run set the old ledger aside")
	}
	legacy, err := isLegacyShape(db, "outbound_dedup")
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
// not read the three-column entity as if it carried `idempotency_key`.
func TestMigrateChanlibLeavesAnAlreadyMigratedDatabaseAlone(t *testing.T) {
	path, db := legacyDB(t, "teled.db", "")
	s := sqlstore.New(db)
	for _, entity := range chanstore.Entities {
		if err := s.CreateEntity(t.Context(), entity); err != nil {
			t.Fatal(err)
		}
	}
	st := chanstore.New(s)
	if _, err := st.ClaimDelivery(t.Context(), "out-1", "telegram:user/1"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordDelivered(t.Context(), "out-1", "plat-1"); err != nil {
		t.Fatal(err)
	}
	if err := migrateChanlib(path, false); err != nil {
		t.Fatalf("a database that holds only entities must be left alone: %v", err)
	}
	// Anti-vacuity: the claim that was there is still there, so "nothing to
	// migrate" did not mean "nothing was read".
	got, err := st.ClaimDelivery(t.Context(), "out-1", "telegram:user/1")
	if err != nil || got.State != chanstore.ClaimSent || got.PlatformID != "plat-1" {
		t.Fatalf("the existing claim came back as (%v, %q, %v)", got.State, got.PlatformID, err)
	}
}

// A row the entities cannot address stops the run, by name, and writes nothing.
// A state no reader understands is one: it would read as "not sent", so the key
// would refuse every later attempt on it forever.
func TestMigrateChanlibRefusesARowNoReaderUnderstands(t *testing.T) {
	path, db := legacyDB(t, "teled.db", chanlibLegacySchema)
	if _, err := db.Exec(
		`INSERT INTO outbound_dedup (idempotency_key, chat_jid, state, created_at)
		 VALUES ('out-odd', 'telegram:user/1', 'queued', '2026-08-21T12:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	err := migrateChanlib(path, false)
	if err == nil {
		t.Fatal("a claim in an unknown state was accepted")
	}
	if !strings.Contains(err.Error(), `state "queued"`) {
		t.Errorf("the refusal %q does not name what is wrong", err)
	}
	if _, qerr := db.Query(`SELECT 1 FROM outbound_dedup_v1`); qerr == nil {
		t.Error("the run set the old ledger aside before it refused")
	}
}

// The discovery is what makes this tool different from its two neighbours:
// chanlib's entities live in one file per CHANNEL, the directory is named for
// the account and the file for the daemon, and an instance may run two of one
// kind. A database the walk missed keeps its old tables and its adapter then
// dedups against nothing.
func TestAdapterDBsFindsEveryChannelIncludingASecondAccount(t *testing.T) {
	storeDir := t.TempDir()
	for _, rel := range []string{
		"teled/teled.db", "teled-rhias/teled.db", "slakd/slakd.db", "emaid/emaid.db",
		"routd/routd.db", // an owner daemon's database, which chanlib never touches
	} {
		full := filepath.Join(storeDir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := db_utils.CreateDBFile(full); err != nil {
			t.Fatal(err)
		}
	}
	// A loose file beside the channel directories is not a channel.
	if err := db_utils.CreateDBFile(filepath.Join(storeDir, "messages.db")); err != nil {
		t.Fatal(err)
	}
	found, err := adapterDBs(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join(storeDir, "emaid/emaid.db"),
		filepath.Join(storeDir, "slakd/slakd.db"),
		filepath.Join(storeDir, "teled-rhias/teled.db"),
		filepath.Join(storeDir, "teled/teled.db"),
	}
	if strings.Join(found, "\n") != strings.Join(want, "\n") {
		t.Fatalf("the walk found\n%s\nwant\n%s",
			strings.Join(found, "\n"), strings.Join(want, "\n"))
	}
}
