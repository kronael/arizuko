package db_utils

import (
	"database/sql"
	"embed"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

// Migrate is the one migration engine every daemon's DB goes through — routd,
// runed, authd, onbod and each adapter. Its 100 lines of exclusive-lock,
// recheck and rollback logic had zero coverage: only the checkVersions guard
// in front of it was tested.

//go:embed testdata
var fixtures embed.FS

// The DSN every owner daemon uses (store/store.go:153). busy_timeout is what
// makes the concurrent case block instead of returning SQLITE_BUSY, so testing
// with a bare path would test a configuration nothing ships.
func openTestDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func appliedVersions(t *testing.T, db *sql.DB, service string) []int {
	t.Helper()
	rows, err := db.Query("SELECT version FROM migrations WHERE service=? ORDER BY version", service)
	if err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func countRows(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow(
		"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

func TestMigrateAppliesEveryFileInOrderAndRecordsIt(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))

	if err := Migrate(db, fixtures, "testdata/ok", "svc"); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if got, want := appliedVersions(t, db, "svc"), []int{1, 2, 3}; len(got) != 3 ||
		got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("recorded versions = %v, want %v", got, want)
	}
	if n := countRows(t, db, "widgets"); n != 3 {
		t.Errorf("widgets has %d rows, want 3 — a migration did not run", n)
	}
	// 0003 depends on 0002 having run; out-of-order application would have
	// errored, so reaching here also proves the ordering.
	if !tableExists(t, db, "gadgets") {
		t.Error("0002 did not run")
	}
}

// A daemon calls Migrate on every boot. The second call must be a no-op: the
// fixture's INSERTs would duplicate rows and its CREATE TABLE would error.
func TestMigrateIsIdempotentAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := openTestDB(t, path)

	for i := 0; i < 3; i++ {
		if err := Migrate(db, fixtures, "testdata/ok", "svc"); err != nil {
			t.Fatalf("Migrate pass %d: %v", i+1, err)
		}
	}
	if n := countRows(t, db, "widgets"); n != 3 {
		t.Errorf("widgets has %d rows after three boots, want 3", n)
	}
	if n := len(appliedVersions(t, db, "svc")); n != 3 {
		t.Errorf("migrations table has %d rows, want 3", n)
	}
}

// A migration set that fails checkVersions must be rejected before anything is
// applied. Applying the valid prefix first and then erroring would leave the DB
// at a version the code does not expect, with no way back.
func TestMigrateRejectsABadSetWithoutTouchingTheDatabase(t *testing.T) {
	for _, dir := range []string{"testdata/dup", "testdata/gap"} {
		t.Run(dir, func(t *testing.T) {
			db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))
			err := Migrate(db, fixtures, dir, "svc")
			if err == nil {
				t.Fatal("Migrate accepted a set checkVersions must reject")
			}
			if tableExists(t, db, "widgets") {
				t.Error("Migrate applied 0001 before failing; the DB is now at an unrecorded version")
			}
			if n := len(appliedVersions(t, db, "svc")); n != 0 {
				t.Errorf("%d migrations recorded for a rejected set", n)
			}
		})
	}
}

// The rollback contract. A migration whose SQL fails must leave nothing behind:
// no partial DDL, no recorded version. Otherwise the next boot skips the file
// and the schema is permanently half-applied on that instance.
func TestMigrateRollsBackAFailedMigrationWhole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := openTestDB(t, path)

	err := Migrate(db, fixtures, "testdata/broken", "svc")
	if err == nil {
		t.Fatal("Migrate reported success on a migration containing invalid SQL")
	}
	if !strings.Contains(err.Error(), "0002-halfway.sql") {
		t.Errorf("the error must name the failing file, got %q", err)
	}

	// 0001 committed on its own and stays.
	if got := appliedVersions(t, db, "svc"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("recorded versions = %v, want just [1]", got)
	}
	if !tableExists(t, db, "widgets") {
		t.Error("0001 was rolled back too; each migration is its own transaction")
	}
	// 0002's first statement must not survive its second statement failing.
	if tableExists(t, db, "half_applied") {
		t.Error("the first half of the failed migration was committed — the DB is half-migrated")
	}

	// And the failure is reported again on the next boot rather than skipped.
	if err := Migrate(db, fixtures, "testdata/broken", "svc"); err == nil {
		t.Error("the second boot reported success; a failed migration must not be silently skipped")
	}
}

// The (service, version) primary key is what lets several daemons share a file
// during the split. One service's progress must not advance another's.
func TestMigrateTracksEachServiceSeparately(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))

	if err := Migrate(db, fixtures, "testdata/ok", "routd"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(db, fixtures, "testdata/other", "onbod"); err != nil {
		t.Fatalf("a second service on the same file: %v", err)
	}
	if got := appliedVersions(t, db, "routd"); len(got) != 3 {
		t.Errorf("routd = %v, want three versions", got)
	}
	if got := appliedVersions(t, db, "onbod"); len(got) != 1 {
		t.Errorf("onbod = %v, want one version", got)
	}
	if !tableExists(t, db, "other_service_table") {
		t.Error("the second service's migration did not run")
	}
}

// A DB migrated partway (a crash, a rollout stopped halfway) must resume from
// where it stopped, not restart. Re-running 0001 would fail on CREATE TABLE.
func TestMigrateResumesFromAPartiallyMigratedDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	db := openTestDB(t, path)

	// Hand-apply 0001 the way a previous release would have left it.
	if _, err := db.Exec(`CREATE TABLE migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		t.Fatal(err)
	}
	raw, err := fixtures.ReadFile("testdata/ok/0001-first.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		"INSERT INTO migrations (service, version, applied_at) VALUES ('svc', 1, '2026-01-01T00:00:00Z')"); err != nil {
		t.Fatal(err)
	}

	if err := Migrate(db, fixtures, "testdata/ok", "svc"); err != nil {
		t.Fatalf("Migrate on a partially migrated DB: %v", err)
	}
	if n := countRows(t, db, "widgets"); n != 3 {
		t.Errorf("widgets has %d rows, want 3", n)
	}
	if got := appliedVersions(t, db, "svc"); len(got) != 3 {
		t.Errorf("recorded versions = %v, want three", got)
	}
}

// The BEGIN EXCLUSIVE + recheck exists for exactly this: two daemons opening
// the same file at once (webd + proxyd on the shared DB, or two containers
// racing a rollout). Each migration must apply exactly once. Without the
// recheck both openers pass the initial MAX(version) read and apply the same
// file twice, which the fixture's CREATE TABLE turns into an error -- or, with
// IF NOT EXISTS DDL, into silently doubled INSERTs.
//
// testdata/slow's first migration holds the exclusive transaction for ~100ms
// so every other opener has certainly read MAX(version)=0 before it commits.
// Against testdata/ok the goroutines simply serialise and neither guard is
// exercised -- removing the recheck, or downgrading BEGIN EXCLUSIVE to BEGIN,
// left that version of this test green.
func TestMigrateAppliesEachFileOnceUnderConcurrentOpeners(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")

	const openers = 6
	dbs := make([]*sql.DB, openers)
	for i := range dbs {
		dbs[i] = openTestDB(t, path)
	}

	var wg sync.WaitGroup
	errs := make([]error, openers)
	start := make(chan struct{})
	for i := range dbs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = Migrate(dbs[i], fixtures, "testdata/slow", "svc")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("opener %d: %v", i, err)
		}
	}
	if t.Failed() {
		t.FailNow()
	}
	if n := countRows(t, dbs[0], "widgets"); n != 3 {
		t.Errorf("widgets has %d rows after %d concurrent migrators, want 3", n, openers)
	}
	if got := appliedVersions(t, dbs[0], "svc"); len(got) != 3 {
		t.Errorf("recorded versions = %v, want exactly three", got)
	}
}

func TestMigrateReportsAMissingDirectory(t *testing.T) {
	db := openTestDB(t, filepath.Join(t.TempDir(), "x.db"))
	err := Migrate(db, fixtures, "testdata/does-not-exist", "svc")
	if err == nil {
		t.Fatal("a typo'd migrations directory must be an error, not an empty successful migration")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("the error must name the directory it could not read, got %q", err)
	}
}

// A migration named without its 4-digit prefix at all. checkVersions reads
// f[:4] to get the version, so anything shorter would panic on the slice.
func TestCheckVersionsRejectsAFilenameTooShortToCarryAVersion(t *testing.T) {
	if err := checkVersions([]string{"a.sql"}); err == nil {
		t.Fatal("want an error for a name with no 4-digit prefix, got nil")
	}
}

// The operator failure this guards is a typo'd mount path. When a parent
// component is a regular file the stat fails with ENOTDIR, not ENOENT — a
// different branch, and one that must still refuse rather than fall through.
func TestRequireDBFileReportsANonExistErrorAsIs(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "routd")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := RequireDBFile(filepath.Join(blocker, "routd.db"))
	if err == nil {
		t.Fatal("a path whose parent is a regular file was accepted")
	}
	if !strings.Contains(err.Error(), "stat ") {
		t.Errorf("want the stat error surfaced, got %q", err)
	}
}

func TestCreateDBFileReportsAnUnusableParent(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "onbod")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateDBFile(filepath.Join(blocker, "onbod.db")); err == nil {
		t.Fatal("CreateDBFile reported success under a parent that is a regular file")
	}
}
