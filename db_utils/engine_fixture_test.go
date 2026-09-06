package db_utils

// The migration engine has to hold on both engines while an instance converts
// (spec 6/37 step 7), so it is tested on both. Every test body in
// migrate_engine_test.go runs once per engine with the same assertions; the two
// statements whose SQL genuinely differs — a placeholder and a catalog probe —
// live here, in the fixture, and nowhere in a test body.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// pgDSNEnv points this suite at a Postgres server. Unset, the suite runs on
// SQLite alone and `make test` keeps its under-five-seconds budget.
const pgDSNEnv = "ARIZUKO_TEST_PG_DSN"

// engineFixture hands a test one database per key. Two calls with one key
// return two handles to the SAME database, which the concurrent-migrator test
// needs.
type engineFixture struct {
	name string
	// open returns a pool on the database named by key, creating it on first use.
	open func(t *testing.T, key string) *sql.DB
	// tableExists answers the engine's catalog.
	tableExists func(t *testing.T, db *sql.DB, table string) bool
	// appliedVersions reads the bookkeeping rows one service has recorded.
	appliedVersions func(t *testing.T, db *sql.DB, service string) []int
}

// forEachEngine runs body against every engine this run can reach.
func forEachEngine(t *testing.T, body func(t *testing.T, e *engineFixture)) {
	t.Helper()
	t.Run("sqlite", func(t *testing.T) { body(t, sqliteFixture(t)) })
	dsn := os.Getenv(pgDSNEnv)
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) { body(t, postgresFixture(t, dsn)) })
}

// dbKey turns a test name into an identifier a database name accepts on either
// engine: readable at the front, and a hash at the back so two long names that
// share a prefix stay distinct inside Postgres's 63-byte limit.
func dbKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r >= 'A' && r <= 'Z':
			return r + ('a' - 'A')
		default:
			return '_'
		}
	}, key)
	if len(safe) > 30 {
		safe = safe[:30]
	}
	return safe + "_" + hex.EncodeToString(sum[:4])
}

func sqliteFixture(t *testing.T) *engineFixture {
	root := t.TempDir()
	return &engineFixture{
		name: "sqlite",
		open: func(t *testing.T, key string) *sql.DB {
			t.Helper()
			// FileDSN is the DSN every owner daemon opens. busy_timeout is what
			// makes the concurrent case block instead of returning SQLITE_BUSY,
			// so a bare path would test a configuration nothing ships.
			db, err := Open(FileDSN(filepath.Join(root, dbKey(key)+".db")))
			if err != nil {
				t.Fatalf("open sqlite %s: %v", key, err)
			}
			t.Cleanup(func() { db.Close() })
			return db
		},
		tableExists: func(t *testing.T, db *sql.DB, table string) bool {
			t.Helper()
			var n int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n > 0
		},
		appliedVersions: func(t *testing.T, db *sql.DB, service string) []int {
			t.Helper()
			return scanVersions(t, db,
				"SELECT version FROM migrations WHERE service=? ORDER BY version", service)
		},
	}
}

func postgresFixture(t *testing.T, adminDSN string) *engineFixture {
	admin, err := Open(adminDSN)
	if err != nil {
		t.Fatalf("open the postgres named by %s: %v", pgDSNEnv, err)
	}
	t.Cleanup(func() { admin.Close() })

	var mu sync.Mutex
	created := map[string]bool{}
	return &engineFixture{
		name: "postgres",
		open: func(t *testing.T, key string) *sql.DB {
			t.Helper()
			name := "arizuko_test_" + dbKey(key)
			mu.Lock()
			first := !created[name]
			created[name] = true
			mu.Unlock()
			if first {
				// A previous run that died before its cleanup leaves the database
				// behind; a test that inherits its rows is a test that proves the
				// wrong thing.
				if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
					t.Fatalf("drop %s: %v", name, err)
				}
				if _, err := admin.Exec("CREATE DATABASE " + name); err != nil {
					t.Fatalf("create %s: %v", name, err)
				}
				t.Cleanup(func() {
					if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name + " WITH (FORCE)"); err != nil {
						t.Errorf("drop %s: %v", name, err)
					}
				})
			}
			db, err := Open(withDatabase(t, adminDSN, name))
			if err != nil {
				t.Fatalf("open postgres %s: %v", name, err)
			}
			t.Cleanup(func() { db.Close() })
			return db
		},
		tableExists: func(t *testing.T, db *sql.DB, table string) bool {
			t.Helper()
			var n int
			if err := db.QueryRow(
				"SELECT COUNT(*) FROM information_schema.tables WHERE table_schema='public' AND table_name=$1",
				table).Scan(&n); err != nil {
				t.Fatal(err)
			}
			return n > 0
		},
		appliedVersions: func(t *testing.T, db *sql.DB, service string) []int {
			t.Helper()
			return scanVersions(t, db,
				"SELECT version FROM migrations WHERE service=$1 ORDER BY version", service)
		},
	}
}

// withDatabase re-points a postgres:// URL at another database on the same
// server, keeping the credentials and every query parameter.
func withDatabase(t *testing.T, dsn, name string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse %s: %v", pgDSNEnv, err)
	}
	u.Path = "/" + name
	return u.String()
}

func scanVersions(t *testing.T, db *sql.DB, query, service string) []int {
	t.Helper()
	rows, err := db.Query(query, service)
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
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
