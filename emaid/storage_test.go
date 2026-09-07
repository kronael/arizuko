package main

// storage_test.go holds the fixture every body that needs a store uses. It goes
// through openStorage rather than through statements: a fixture that spelled
// the storage out in SQL would be asserting on ONE driver's table shape while
// the adapter it tests holds none.

import (
	"database/sql"
	"testing"

	"github.com/kronael/arizuko/emaid/mailstore"
)

// newTestStorage hands a test emaid's two halves over a fresh emaid.db: the
// pool chanlib dedups against, and the store emaid reads.
func newTestStorage(t *testing.T) (*sql.DB, *mailstore.Store) {
	t.Helper()
	db, st, err := openStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, st
}

// newTestMailstore is newTestStorage for a body that needs only the store — the
// poller, which touches nothing else.
func newTestMailstore(t *testing.T) *mailstore.Store {
	t.Helper()
	_, st := newTestStorage(t)
	return st
}
