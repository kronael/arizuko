package main

// storage.go is where emaid picks its storage driver and makes sure the store
// can answer. It is the composition root and the only file in the adapter that
// names a driver: mailstore holds the interface, so a second engine is a second
// driver and one line here (spec 6/37 step 5).
//
// There is no migration set and no DDL of emaid's own. An entity is a name the
// driver resolves however it must — a table on SQL, nothing at all on an engine
// with no schema — so preparing the storage is asking the driver for the
// entities mailstore declares, and asking twice is a no-op.

import (
	"context"
	"database/sql"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/emaid/mailstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	_ "modernc.org/sqlite"
)

// openStorage opens <dataDir>/emaid.db with the owner-DB conventions (WAL,
// busy_timeout, foreign keys on every pooled connection) and prepares it to
// serve emaid. It creates a missing file itself: emaid.db is adapter-private
// state, and `arizuko create` does not seed it.
//
// It opens through chanlib's own adapter-database door, the same one the seven
// other adapters use, so emaid.db is prepared exactly the way theirs are. That
// door hands back the pool as well as chanlib's store, which is what lets emaid
// put its OWN entities in the same file — the alternative was a second opener
// for the one daemon that needs the pool, and two openers drift.
//
// It hands back all three halves because emaid needs all three: the pool to
// close, chanlib's store for the adapter mux's outbound dedup, and mailstore's
// for emaid's own threading.
func openStorage(dataDir string) (*sql.DB, *chanstore.Store, *mailstore.Store, error) {
	db, chanStore, err := chanlib.OpenAdapterDB(dataDir, "emaid")
	if err != nil {
		return nil, nil, nil, err
	}
	s := sqlstore.New(db)
	for _, entity := range mailstore.Entities {
		if err := s.CreateEntity(context.Background(), entity); err != nil {
			db.Close() //nolint:errcheck // the open already failed
			return nil, nil, nil, err
		}
	}
	return db, chanStore, mailstore.New(s), nil
}
