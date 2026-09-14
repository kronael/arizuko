package main

// storage.go is where emaid picks its storage driver and makes sure the store
// can answer. It is the composition root and the only file in the adapter that
// names a driver: emaistore holds the interface, so a second engine is a second
// driver and one line here (spec 6/37 step 5).
//
// There is no migration set and no DDL of emaid's own. An entity is a name the
// driver resolves however it must — a table on SQL, nothing at all on an engine
// with no schema — so preparing the storage is asking the driver for the
// entities emaistore declares, and asking twice is a no-op.

import (
	"context"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/emaid/emaistore"
	_ "modernc.org/sqlite"
)

// openStorage prepares emaid's storage: the entities chanlib ships and the ones
// emaid ships of its own, on one store. On a file instance that store is
// <dataDir>/emaid.db with the owner-DB conventions (WAL, busy_timeout, foreign
// keys on every pooled connection), created here because it is adapter-private
// state `arizuko create` does not seed.
//
// It opens through chanlib's own adapter door, the same one the seven other
// adapters use, so emaid's tables are prepared exactly the way theirs are and
// the branch between an endpoint and a file is not written twice. That door
// hands back the driver as well as chanlib's store, which is what lets emaid
// put its OWN entities on the same one.
//
// It hands back all three because emaid needs all three: the close, chanlib's
// store for the adapter mux's outbound dedup, and emaistore's for emaid's own
// threading.
func openStorage(dataDir string) (chanlib.AdapterStore, *emaistore.Store, error) {
	as, err := chanlib.OpenAdapterStore("emaid")
	if err != nil {
		return chanlib.AdapterStore{}, nil, err
	}
	for _, entity := range emaistore.Entities {
		if err := as.Driver.CreateEntity(context.Background(), entity); err != nil {
			as.Close() //nolint:errcheck // the open already failed
			return chanlib.AdapterStore{}, nil, err
		}
	}
	return as, emaistore.New(as.Driver), nil
}
