package main

// storage.go is authd's composition root: the only file in the daemon that
// names a storage driver (spec 6/37 step 5). There is no migration set and no
// DDL; creating the storage is asking the driver for the entities authstore
// declares, and asking twice is a no-op.

import (
	"context"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/store"
)

// ensureStorage prepares d to serve authd: every entity authstore reads, and
// the one audit records into. Without the audit entity every Emit would warn
// and drop the row while the daemon reported healthy (BUGS F29).
func ensureStorage(d store.Driver) error {
	ctx := context.Background()
	for _, entity := range authstore.Entities {
		if err := d.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return audit.EnsureEntities(ctx, d)
}
