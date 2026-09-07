package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// openEntities opens a store holding ONLY the entities — no migration chain at
// all. That is what makes the moved tables testable on a second engine: OpenMem
// runs the FROZEN pre-split chain, whose 0001 uses AUTOINCREMENT, so a Postgres
// database refuses it before any entity code runs (the L228 boundary, and step
// 8's job rather than this one's).
//
// It is also the right subject. A test that opened the frozen chain to reach an
// entity would be asserting against a schema no daemon has, which is the mistake
// TestMigrationsAreFrozen's own header records (BUGS Q1). An entity needs a
// driver and a declaration, and this is both.
//
// db_utils.OpenTestDB owns the engine and the DSN, so ARIZUKO_TEST_PG_DSN moves
// this and every other test database together (spec 6/37 step 6).
func openEntities(t *testing.T) *Store {
	t.Helper()
	db, release, err := db_utils.OpenTestDB("store-entities")
	if err != nil {
		t.Fatalf("open a test database: %v", err)
	}
	t.Cleanup(func() {
		if err := release(); err != nil {
			t.Errorf("release the test database: %v", err)
		}
	})
	if err := EnsureEntities(context.Background(), sqlstore.New(db)); err != nil {
		t.Fatalf("create the entities: %v", err)
	}
	return New(db)
}
