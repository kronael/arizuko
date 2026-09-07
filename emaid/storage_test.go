package main

// storage_test.go holds the fixture every body that needs a store uses. It goes
// through openStorage rather than through statements: a fixture that spelled
// the storage out in SQL would be asserting on ONE driver's table shape while
// the adapter it tests holds none.

import (
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/emaid/emaistore"
)

// newTestStorage hands a test the two stores over a fresh emaid.db: chanlib's,
// which the adapter mux dedups against, and emaistore's, which emaid reads.
func newTestStorage(t *testing.T) (*chanstore.Store, *emaistore.Store) {
	t.Helper()
	db, chanStore, st, err := openStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return chanStore, st
}

// newTestMailstore is newTestStorage for a body that needs only the store — the
// poller, which touches nothing else.
func newTestMailstore(t *testing.T) *emaistore.Store {
	t.Helper()
	_, st := newTestStorage(t)
	return st
}
