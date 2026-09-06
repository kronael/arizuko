package db_utils

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// A SQLite path and a Postgres URL reach the same function, so the branch that
// tells them apart is the whole engine choice. Getting it wrong on a Postgres
// DSN means SQLite creating a file named after the connection string and the
// daemon booting green on an empty schema.
func TestOpenSelectsTheEngineFromTheDSN(t *testing.T) {
	db := mustOpen(t, FileDSN(filepath.Join(t.TempDir(), "x.db")))
	if got := dialectFor(db); got.selectVersion != sqliteDialect.selectVersion {
		t.Errorf("a file path opened %v, want SQLite", got.selectVersion)
	}

	for _, dsn := range []string{
		"host=db user=routd dbname=routd sslmode=disable",
		"dbname=routd host=db",
		"user=routd",
	} {
		_, err := Open(dsn)
		if err == nil {
			t.Errorf("Open(%q) succeeded; a libpq keyword DSN must be refused, not "+
				"opened as a SQLite file with that name", dsn)
			continue
		}
		if !strings.Contains(err.Error(), "postgres:// URL") {
			t.Errorf("Open(%q) error must say what form to use, got %q", dsn, err)
		}
	}
}

// sql.Open on a Postgres DSN connects to nothing, so a daemon handed a wrong
// host, port or password would boot green and fail on its first query — the
// same silent-green failure RequireDBFile refuses on the SQLite side.
func TestOpenRefusesAnUnreachablePostgres(t *testing.T) {
	// Port 1 refuses immediately, so this needs no server and cannot hang.
	_, err := Open("postgres://arizuko@127.0.0.1:1/nope?sslmode=disable&connect_timeout=2")
	if err == nil {
		t.Fatal("Open returned a handle to a Postgres it never reached")
	}
	if !strings.Contains(err.Error(), "connect to postgres") {
		t.Errorf("the error must say the connection failed, got %q", err)
	}
}

// WAL is a hard error, not a warning: a store in rollback-journal mode looks
// identical to a healthy one and serialises every reader against the writer.
func TestOpenPutsSQLiteInWAL(t *testing.T) {
	db := mustOpen(t, FileDSN(filepath.Join(t.TempDir(), "x.db")))
	var mode string
	if err := db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// The retry rule admits remote calls and a database that is busy, locked,
// serialization-failed or deadlocked. Everything else — a unique violation, a
// syntax error, a missing column — is a defect a retry repeats.
func TestIsTransientAcceptsOnlyTheRetryableClass(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"a plain error", errors.New("boom"), false},
		{"40001 serialization failure", &pgconn.PgError{Code: "40001"}, true},
		{"40P01 deadlock detected", &pgconn.PgError{Code: "40P01"}, true},
		{"23505 unique violation", &pgconn.PgError{Code: "23505"}, false},
		{"42601 syntax error", &pgconn.PgError{Code: "42601"}, false},
		{"42P01 undefined table", &pgconn.PgError{Code: "42P01"}, false},
		{"wrapped 40P01", wrapped(&pgconn.PgError{Code: "40P01"}), true},
	}
	for _, c := range cases {
		if got := IsTransient(c.err); got != c.want {
			t.Errorf("IsTransient(%s) = %v, want %v", c.name, got, c.want)
		}
	}
}

func wrapped(err error) error {
	return errStack{err}
}

type errStack struct{ err error }

func (e errStack) Error() string { return "outer: " + e.err.Error() }
func (e errStack) Unwrap() error { return e.err }

// The table above asserts a mapping; this asserts the driver really hands that
// error type through database/sql. A classifier that never matches the shape it
// is given is green on a table and useless in production.
func TestIsTransientSeesARealSQLiteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")
	holder := mustOpen(t, FileDSN(path))
	if _, err := holder.Exec("CREATE TABLE t (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	// busy_timeout(0) turns the wait every daemon does into the error the
	// classifier has to name.
	blocked := mustOpen(t, path+"?_pragma=busy_timeout(0)")

	tx, err := holder.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() //nolint:errcheck — the write lock is released either way
	if _, err := tx.Exec("INSERT INTO t (id) VALUES (1)"); err != nil {
		t.Fatal(err)
	}

	_, err = blocked.Exec("INSERT INTO t (id) VALUES (2)")
	if err == nil {
		t.Fatal("a second writer got the lock; the test proves nothing")
	}
	if !IsTransient(err) {
		t.Errorf("IsTransient did not recognise a real SQLite lock: %v", err)
	}
}

// Deadlock is the class SQLite has no counterpart for — one writer per file
// means two transactions can never wait on each other — so it is the one a
// SQLite-only test can never cover.
func TestIsTransientSeesARealPostgresDeadlock(t *testing.T) {
	dsn := os.Getenv(TestPGDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", TestPGDSNEnv)
	}
	db := mustOpen(t, dsn)
	if _, err := db.Exec(`DROP TABLE IF EXISTS deadlock_probe;
		CREATE TABLE deadlock_probe (id int PRIMARY KEY, v int NOT NULL);
		INSERT INTO deadlock_probe VALUES (1, 0), (2, 0)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec("DROP TABLE IF EXISTS deadlock_probe"); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
	})

	ctx := context.Background()
	first, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	bump := func(tx *sql.Tx, id int) error {
		_, err := tx.ExecContext(ctx, "UPDATE deadlock_probe SET v = v + 1 WHERE id = $1", id)
		return err
	}
	if err := bump(first, 1); err != nil {
		t.Fatal(err)
	}
	if err := bump(second, 2); err != nil {
		t.Fatal(err)
	}

	// Each now waits on the row the other holds. Postgres breaks the cycle
	// after deadlock_timeout and fails exactly one of them with 40P01.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = bump(first, 2) }()
	go func() { defer wg.Done(); errs[1] = bump(second, 1) }()
	wg.Wait()
	first.Rollback()  //nolint:errcheck — one of the two is already aborted
	second.Rollback() //nolint:errcheck — one of the two is already aborted

	var loser error
	for _, err := range errs {
		if err != nil {
			loser = err
		}
	}
	if loser == nil {
		t.Fatal("neither transaction was broken; no deadlock happened")
	}
	if !IsTransient(loser) {
		t.Errorf("IsTransient did not recognise a real Postgres deadlock: %v", loser)
	}
}

// Daemons with unbounded pools is what a managed instance rejects first, so the
// cap is not advisory — and the number that matters is the fleet's total, not
// one pool's. Asserting the constant against itself would prove nothing.
func TestOpenCapsThePostgresPool(t *testing.T) {
	dsn := os.Getenv(TestPGDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", TestPGDSNEnv)
	}
	db := mustOpen(t, dsn)
	// database/sql reports 0 for "no limit".
	if got := db.Stats().MaxOpenConnections; got <= 0 {
		t.Errorf("MaxOpenConnections = %d; an unbounded pool is what a managed instance rejects", got)
	}
	// fleetPools is the deployment's pool count with room for four more
	// adapters; stockBudget is what max_connections=100 leaves after
	// superuser_reserved_connections=3.
	const fleetPools, stockBudget = 24, 97
	if total := fleetPools * maxOpenConns; total > stockBudget {
		t.Errorf("%d pools of %d ask one server for %d connections, past the %d a stock "+
			"Postgres leaves", fleetPools, maxOpenConns, total, stockBudget)
	}
}

// A transaction-mode pooler refuses PREPARE/DEALLOCATE and hands each
// transaction a different server connection, so a statement pgx prepared on one
// is missing on the next. pg_prepared_statements is per-session, so the probe
// and the parameterised query it judges have to share a connection.
//
// A statement with no arguments takes the simple protocol in every exec mode
// (pgx conn.go: "Always use simple protocol when there are no arguments"), so
// only a parameterised one can tell the modes apart.
func TestOpenRunsPostgresOnTheSimpleProtocol(t *testing.T) {
	dsn := os.Getenv(TestPGDSNEnv)
	if dsn == "" {
		t.Skipf("%s not set", TestPGDSNEnv)
	}
	db := mustOpen(t, dsn)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close() //nolint:errcheck — returning the connection cannot fail the assertion

	var n int
	if err := conn.QueryRowContext(ctx, "SELECT $1::int", 7).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Fatalf("SELECT $1::int returned %d", n)
	}
	var prepared int
	if err := conn.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM pg_prepared_statements").Scan(&prepared); err != nil {
		t.Fatal(err)
	}
	if prepared != 0 {
		t.Errorf("the session holds %d prepared statements; a transaction-mode pooler "+
			"gives the next transaction a connection that has none of them", prepared)
	}
}

func mustOpen(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
