// Package dbtest hands a test an empty database. It is the only way a _test.go
// reaches an engine: db_utils/opener_test.go fails the build on a direct
// sql.Open anywhere else, so pointing the suite at another engine is one
// environment variable rather than forty edits (spec 6/37 step 6).
package dbtest

import (
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/db_utils"
)

// Open returns an empty database private to t, released when t finishes. Two
// calls in one test return two databases; ARIZUKO_TEST_PG_DSN decides the engine
// for both, in db_utils and nowhere else.
func Open(t testing.TB) *sql.DB {
	t.Helper()
	db, release, err := db_utils.OpenTestDB(t.Name())
	if err != nil {
		t.Fatalf("open a test database: %v", err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release the test database: %v", err)
		}
	})
	return db
}
