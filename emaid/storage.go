package main

// storage.go is where emaid picks its storage driver and makes sure the store
// can answer. It is the composition root and the only file in the adapter that
// names a driver: emaistore holds the interface, so a second engine is a second
// driver and one line here (objstore/README.md).
//
// There is no migration set and no DDL of emaid's own. An entity is a name the
// driver resolves however it must — a table on SQL, nothing at all on an engine
// with no schema — so preparing the storage is asking the driver for the
// entities emaistore declares, and asking twice is a no-op.

import (
	"context"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/emaid/emaistore"
)

func openStorage() (chanlib.AdapterStore, *emaistore.Store, error) {
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
