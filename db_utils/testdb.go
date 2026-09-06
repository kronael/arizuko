package db_utils

// The test face of the opener. A test asks this package for a database the same
// way a daemon does — by DSN — so ONE environment variable moves the whole suite
// between engines (spec 6/37 step 6, "the same suite passes against both").
//
// It lives beside the opener rather than in a test-only package because
// store.OpenMem, routd.OpenMem and runed.OpenMem call it from production files,
// and a production package must not link `testing`. db_utils/dbtest is the
// *testing.T wrapper every _test.go uses.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

// TestPGDSNEnv points the suite at a Postgres server, as a URL to the database
// the test databases are created beside. Unset, every test database is SQLite
// and `make test` keeps its under-five-seconds budget.
const TestPGDSNEnv = "ARIZUKO_TEST_PG_DSN"

// testDBSeq distinguishes the databases one test binary reserves.
var testDBSeq atomic.Int64

// OpenTestDB is NewTestDB plus Open, for a caller that has no DSN-taking opener
// of its own. The returned function closes the pool and releases the database.
func OpenTestDB(name string) (*sql.DB, func() error, error) {
	dsn, drop, err := NewTestDB(name)
	if err != nil {
		return nil, nil, err
	}
	db, err := Open(dsn)
	if err != nil {
		// The database was reserved before the pool failed; leaving it behind
		// costs the next run a name collision on Postgres.
		if derr := drop(); derr != nil {
			return nil, nil, fmt.Errorf("open test database: %w (and releasing it: %v)", err, derr)
		}
		return nil, nil, err
	}
	release := func() error {
		cerr := db.Close()
		derr := drop()
		if cerr != nil {
			return cerr
		}
		return derr
	}
	return db, release, nil
}

// NewTestDB reserves an empty database for one test and returns its DSN with the
// function that releases it. TestPGDSNEnv decides the engine: unset, the database
// is a private SQLite one held in memory; set, it is a fresh database on the
// Postgres server that DSN names.
//
// Every call reserves a distinct database, so two tests never share rows and a
// test that wants a second one calls this again. It hands back a DSN rather than
// a pool so an owner daemon reaches it through the opener it already has.
func NewTestDB(name string) (string, func() error, error) {
	dbName := "arizuko_test_" + safeIdent(name) + "_" + uniqueSuffix(name)
	if adminDSN := os.Getenv(TestPGDSNEnv); adminDSN != "" {
		return createPostgresDB(adminDSN, dbName)
	}
	// A bare `:memory:` database is per CONNECTION and database/sql pools many,
	// so a second query lands on an empty one. A named memdb with cache=shared is
	// shared across this pool's connections and private to this DSN. The pragma
	// set is production's, so a test runs the configuration a daemon runs.
	dsn := "file:" + dbName + "?mode=memory&cache=shared" +
		"&_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	return dsn, func() error { return nil }, nil
}

// createPostgresDB creates database name on the server adminDSN names and returns
// a DSN for it with the function that drops it. A database a run left behind when
// it died before its cleanup is dropped first: a test that inherits its rows
// proves the wrong thing.
func createPostgresDB(adminDSN, name string) (string, func() error, error) {
	if err := onAdmin(adminDSN, func(admin *sql.DB) error {
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
			return fmt.Errorf("drop %s: %w", name, err)
		}
		if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
			return fmt.Errorf("create %s: %w", name, err)
		}
		return nil
	}); err != nil {
		return "", nil, err
	}
	dsn, err := withDatabase(adminDSN, name)
	if err != nil {
		return "", nil, err
	}
	drop := func() error {
		return onAdmin(adminDSN, func(admin *sql.DB) error {
			_, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)")
			return err
		})
	}
	return dsn, drop, nil
}

// onAdmin runs body against the server TestPGDSNEnv names. CREATE DATABASE and
// DROP DATABASE run outside a transaction and outside the database they name, so
// they need a connection to another one.
func onAdmin(adminDSN string, body func(*sql.DB) error) error {
	admin, err := Open(adminDSN)
	if err != nil {
		return fmt.Errorf("open the postgres named by %s: %w", TestPGDSNEnv, err)
	}
	// the admin pool exists for this one statement; a close error hides nothing
	defer admin.Close() //nolint:errcheck
	return body(admin)
}

// withDatabase re-points a postgres:// URL at another database on the same
// server, keeping the credentials and every query parameter.
func withDatabase(dsn, name string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", TestPGDSNEnv, err)
	}
	u.Path = "/" + name
	return u.String(), nil
}

// uniqueSuffix makes a database name distinct per call AND per test binary:
// `go test ./...` runs package binaries concurrently, so two packages holding a
// test of the same name would otherwise reserve one Postgres database.
func uniqueSuffix(name string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s/%s/%d",
		filepath.Base(os.Args[0]), name, testDBSeq.Add(1))))
	return hex.EncodeToString(sum[:4])
}

// dbKey turns a key into an identifier a database name accepts on either engine:
// readable at the front, and a hash at the back so two long keys that share a
// prefix stay distinct inside Postgres's 63-byte limit.
func dbKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return safeIdent(key) + "_" + hex.EncodeToString(sum[:4])
}

// safeIdent keeps the readable head of a database name: lower case, letters,
// digits and underscore, short enough to leave room for a hash and a prefix.
func safeIdent(s string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, s)
	if len(safe) > 30 {
		safe = safe[:30]
	}
	return safe
}
