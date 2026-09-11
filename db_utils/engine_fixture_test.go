package db_utils

// The migration engine has to hold on both engines while an instance converts
// (spec 6/37 step 7), so it is tested on both. Every test body in
// migrate_engine_test.go runs once per engine with the same assertions; the two
// statements whose SQL genuinely differs — a placeholder and a catalog probe —
// live here, in the fixture, and nowhere in a test body.

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

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
	dsn := os.Getenv(TestPGDSNEnv)
	if dsn == "" {
		return
	}
	t.Run("postgres", func(t *testing.T) { body(t, postgresFixture(t, dsn)) })
}

func sqliteFixture(t *testing.T) *engineFixture {
	root := t.TempDir()
	return &engineFixture{
		name: "sqlite",
		open: func(t *testing.T, key string) *sql.DB {
			t.Helper()
			// Open adds the pragma set every owner daemon runs. busy_timeout is
			// what makes the concurrent case block instead of returning
			// SQLITE_BUSY, so a database without it would test a configuration
			// nothing ships.
			db, err := Open(filepath.Join(root, dbKey(key)+".db"))
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
	// One DSN per database name, so two calls with one key open two pools on the
	// SAME database — what the concurrent-migrator test needs.
	var mu sync.Mutex
	dsns := map[string]string{}
	return &engineFixture{
		name: "postgres",
		open: func(t *testing.T, key string) *sql.DB {
			t.Helper()
			name := "arizuko_test_" + dbKey(key)
			mu.Lock()
			dsn, held := dsns[name]
			if !held {
				var drop func() error
				var err error
				if dsn, drop, err = createPostgresDB(adminDSN, name); err != nil {
					mu.Unlock()
					t.Fatalf("reserve %s: %v", name, err)
				}
				dsns[name] = dsn
				t.Cleanup(func() {
					if err := drop(); err != nil {
						t.Errorf("drop %s: %v", name, err)
					}
				})
			}
			mu.Unlock()
			db, err := Open(dsn)
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
