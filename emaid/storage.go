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
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/emaid/mailstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	_ "modernc.org/sqlite"
)

// openStorage opens <dataDir>/emaid.db with the owner-DB conventions (WAL,
// busy_timeout, foreign keys on every pooled connection) and prepares it to
// serve emaid. It creates a missing file itself: emaid.db is adapter-private
// state, and `arizuko create` does not seed it.
//
// It hands back both halves because emaid needs both. The pool is chanlib's:
// `outbound_dedup` lands in this same file and chanlib runs its own statements
// against it, which is the ownership half of the seam (BUGS L251) and moves
// when chanlib does. The store is emaid's own, and it is all emaid touches.
func openStorage(dataDir string) (*sql.DB, *mailstore.Store, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, nil, err
	}
	db, err := db_utils.Open(db_utils.FileDSN(filepath.Join(dataDir, "emaid.db")))
	if err != nil {
		return nil, nil, err
	}
	if err := chanlib.MigrateAdapterDB(db); err != nil {
		db.Close()
		return nil, nil, err
	}
	s := sqlstore.New(db)
	for _, entity := range mailstore.Entities {
		if err := s.CreateEntity(context.Background(), entity); err != nil {
			db.Close()
			return nil, nil, err
		}
	}
	return db, mailstore.New(s), nil
}
