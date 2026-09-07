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
	"database/sql"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
)

// ensureStorage prepares db to serve authd: audit's own table, then every
// entity authstore reads.
//
// audit_log is the one thing here that is still SQL. authd hands its raw
// *sql.DB to audit.Emit, audit.Query and store.New, which run their own
// statements against it — the ownership half of the seam, tracked as BUGS L251
// — so the table they need is created by the package that writes it.
func ensureStorage(db *sql.DB) error {
	ctx := context.Background()
	if err := audit.EnsureTable(ctx, db); err != nil {
		return err
	}
	s := sqlstore.New(db)
	for _, entity := range authstore.Entities {
		if err := s.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return nil
}

// newAuthstore is the driver choice, in one place. sqlstore serves both SQLite
// and Postgres off the DSN db_utils.Open already read, so this line does not
// change when the engine does.
func newAuthstore(db *sql.DB) *authstore.Store { return authstore.New(sqlstore.New(db)) }
