package main

import (
	"context"
	"database/sql"

	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

// newObjectStore is the driver a pool is reached through. store.OwnerDriver
// picks it for the running daemon; this is what a test holding a pool of its
// own builds.
func newObjectStore(db *sql.DB) *sqlstore.Store { return sqlstore.New(db) }

// openOwned prepares the store that holds onbod's OWNED tables (onboarding,
// invites, onboarding_gates, invite_redemptions) + its own audit_log, and hands
// back the questions onbod asks of them.
//
// store.OwnerDriver is the door under it: on an instance served from a
// DynamoDB endpoint it dials onbod's tables there, and otherwise it opens the
// database ownDSN names — a SQLite path or a postgres:// URL, whichever onbod
// was configured with. It is also where the strictness lives: onbod never
// creates its own database, and SQLite creates a missing file on first query,
// so a wrong path would build a fresh empty schema and drop every invite and
// gate while onbod reports healthy (spec 5/16 step 7, BUGS F52).
func openOwned(ownDSN string) (*store.Store, error) {
	d, closeStore, err := store.OwnerDriver(store.OwnerOnbod, ownDSN)
	if err != nil {
		return nil, err
	}
	if err := prepareOwned(d); err != nil {
		closeStore() //nolint:errcheck // the open already failed
		return nil, err
	}
	return store.NewOver(d), nil
}

// prepareOwned builds what onbod owns on d: the entities it reads and writes.
//
// ONBOD HAS NO MIGRATION CHAIN. Every table it owns is an objstore entity, and
// an entity is declared beside the code that reads it and built by the driver —
// so there is no DDL for a chain to carry and no version for one to record
// (spec 6/37 step 7). authd, runed, chanlib and emaid own their databases the
// same way.
//
// Split out of openOwned so a test builds what onbod ships from the one
// declaration of it, rather than a second one that drifts.
func prepareOwned(d store.Driver) error {
	// TWO CALLS, TWO OWNERS. store owns the declaration of onboarding,
	// onboarding_gates and invites, because store is where they are read and
	// written, and it carries audit's entity along with them — every audited
	// write needs somewhere to record. resreg owns the version MARKER its
	// replace claims and its consistent read compares.
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, d, store.OwnerOnbod); err != nil {
		return err
	}
	return resreg.EnsureSubsystem(ctx, d, d, resreg.SubsystemOnbod)
}
