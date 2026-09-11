package db_utils

// The pragma set is what makes ON DELETE CASCADE fire and what makes a busy
// database wait instead of erroring, and Open is the one place it is applied —
// so a caller passes the target it was configured with and never a DSN it built.

import (
	"path/filepath"
	"strings"
	"testing"
)

// Every SQLite database arizuko opens enforces foreign keys. The default is OFF
// per CONNECTION and database/sql pools many, so a probe has to run on a
// connection the pool hands out rather than on one this test opened itself.
func TestOpenEnforcesForeignKeysOnAPlainPath(t *testing.T) {
	db := mustOpen(t, filepath.Join(t.TempDir(), "x.db"))
	var on int
	if err := db.QueryRow("PRAGMA foreign_keys").Scan(&on); err != nil {
		t.Fatal(err)
	}
	if on != 1 {
		t.Fatal("foreign_keys is off, so every ON DELETE CASCADE in the tree is a no-op")
	}
}

// A test database is a `file:` URI that already carries query parameters. The
// pragma set has to join them rather than replace them, or the memdb stops being
// shared across the pool and a second query lands on an empty database.
func TestOpenKeepsTheParametersTheTargetAlreadyCarries(t *testing.T) {
	dsn, err := sqliteDSN("file:memdb1?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=memory", "cache=shared", "busy_timeout(5000)", "foreign_keys(on)"} {
		if !strings.Contains(dsn, want) {
			t.Errorf("%q is missing %q", dsn, want)
		}
	}
}

// The driver SORTS the pragma set before applying it, so a DSN naming
// busy_timeout twice does not override — which of the two lands is unspecified.
// Open therefore supplies only what the target does not already name.
func TestOpenLeavesAPragmaTheTargetAlreadyNames(t *testing.T) {
	dsn, err := sqliteDSN("x.db?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dsn, "busy_timeout(5000)") {
		t.Errorf("%q adds a second busy_timeout; the driver sorts them and the winner is unspecified", dsn)
	}
	if !strings.Contains(dsn, "foreign_keys(on)") {
		t.Errorf("%q dropped foreign_keys, which the target did not name", dsn)
	}
}

// RequireDB is the guard a daemon runs before Open, and a Postgres target is not
// a file. Statting one would refuse every Postgres DSN at boot.
func TestRequireDBAsksNothingOfAPostgresTarget(t *testing.T) {
	for _, target := range []string{
		"postgres://arizuko@db:5432/routd",
		"postgresql://arizuko@db:5432/routd?sslmode=disable",
	} {
		if err := RequireDB(target); err != nil {
			t.Errorf("RequireDB(%q) = %v; a Postgres server refuses a database that is "+
				"not there and Open's ping carries that, so there is nothing to stat", target, err)
		}
	}
}

// The same guard still refuses a SQLite path that is not there: that is the
// whole reason it exists (spec 5/16 step 7).
func TestRequireDBStillRefusesAMissingFile(t *testing.T) {
	if err := RequireDB(filepath.Join(t.TempDir(), "absent.db")); err == nil {
		t.Fatal("RequireDB accepted a path with no database; SQLite would create it and " +
			"the daemon would boot green holding none of the operator's data")
	}
}
