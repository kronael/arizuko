package main

import (
	"context"
	"database/sql"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// newObjectStore is onbod's driver choice, in one place: sqlstore serves both
// SQLite and Postgres off the DSN db_utils.Open already read.
func newObjectStore(db *sql.DB) *sqlstore.Store { return sqlstore.New(db) }

// openOwnedDB opens the database that holds onbod's OWNED tables (onboarding,
// invites, onboarding_gates, invite_redemptions) + its own audit_log, and builds
// their schema. ownDSN is whatever onbod was configured with — a SQLite path or
// a postgres:// URL — and db_utils.Open reads the engine off it.
func openOwnedDB(ownDSN string) (*sql.DB, error) {
	// Strict: onbod never creates its own database. SQLite creates a missing file
	// on first query, so a wrong path would build a fresh empty schema and drop
	// every invite and gate while onbod reports healthy (spec 5/16 step 7, BUGS
	// F52). `arizuko create` seeds the file.
	if err := db_utils.RequireDB(ownDSN); err != nil {
		return nil, err
	}
	db, err := db_utils.Open(ownDSN)
	if err != nil {
		return nil, err
	}
	if err := migrateOwned(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrateOwned builds onbod's OWN schema in db: the entities it reads and writes.
//
// ONBOD HAS NO MIGRATION CHAIN. Every table it owns is an objstore entity, and
// an entity is declared beside the code that reads it and built by the driver —
// so there is no DDL for a chain to carry and no version for one to record
// (spec 6/37 step 7). authd, runed, chanlib and emaid own their databases the
// same way.
//
// Split out of openOwnedDB so a test builds the schema onbod ships from the one
// declaration of it, rather than a second one that drifts.
func migrateOwned(db *sql.DB) error {
	// THREE CALLS, THREE OWNERS. audit owns its entity and four databases carry
	// it. store owns the declaration of onboarding/onboarding_gates/invites,
	// because store is where they are read and written. resreg owns the version
	// MARKER its replace claims and its consistent read compares — and the
	// marker has to exist before any write, because a Bump is an Update and an
	// Update against a missing item refuses the whole batch it rides in
	// (objstore.EnsureScope).
	ctx := context.Background()
	obj := newObjectStore(db)
	if err := audit.EnsureEntities(ctx, obj); err != nil {
		return err
	}
	if err := store.EnsureEntities(ctx, obj, store.OwnerOnbod); err != nil {
		return err
	}
	return resreg.EnsureSubsystem(ctx, obj, obj, resreg.SubsystemOnbod)
}
