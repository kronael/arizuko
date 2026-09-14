package main

// storage.go is where authd picks its storage driver and makes sure the store
// can answer. It is the composition root and the only file in the daemon that
// names a driver: authstore holds the interface, so a second engine is a second
// driver and one line here (spec 6/37 step 5).
//
// There is no migration set and no DDL. An entity is a name the driver resolves
// however it must — a table on SQL, nothing at all on an engine with no schema
// — so creating the storage is asking the driver for the entities authstore
// declares, and asking twice is a no-op.

import (
	"context"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/store"
)

// ensureStorage prepares d to serve authd: every entity authstore reads, and
// the one audit records into.
//
// audit declares its entity beside the code that writes it and every owner
// applies the same list, so a daemon with no migrations of its own still has
// somewhere to record — without which every Emit would warn and drop the row
// while the daemon reported healthy (BUGS F29 is what that costs).
func ensureStorage(d store.Driver) error {
	ctx := context.Background()
	for _, entity := range authstore.Entities {
		if err := d.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return audit.EnsureEntities(ctx, d)
}
