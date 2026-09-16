package main

// storage_test.go holds the fixture every body that needs a store uses. It asks
// for the entity sets the two layers DECLARE rather than spelling storage out
// itself, so a set that changes reaches these bodies. openStorage's own door is
// proved once, in config_test.go, on the prefix a live adapter opens.

import (
	"testing"

	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
)

// newTestStorage hands a test the two stores over a namespace of its own:
// chanlib's, which the adapter mux dedups against, and emaistore's, which emaid
// reads.
func newTestStorage(t *testing.T) (*chanstore.Store, *emaistore.Store) {
	t.Helper()
	d := dyntest.Store(t)
	for _, entity := range append(append([]string{}, chanstore.Entities...), emaistore.Entities...) {
		if err := d.CreateEntity(t.Context(), entity); err != nil {
			t.Fatalf("create entity %s: %v", entity, err)
		}
	}
	return chanstore.New(d), emaistore.New(d)
}

// newTestMailstore is newTestStorage for a body that needs only the store — the
// poller, which touches nothing else.
func newTestMailstore(t *testing.T) *emaistore.Store {
	t.Helper()
	_, st := newTestStorage(t)
	return st
}
