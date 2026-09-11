package main

import (
	"context"
	"database/sql"
	"embed"

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

//go:embed migrations/*.sql
var onbodMigrationFS embed.FS

const onbodServiceName = "onbod"

// openOwnedDB opens the DB that holds onbod's OWNED tables (onboarding, invites,
// onboarding_gates) + its own audit_log, running the onbod migration sequence.
// ownDSN points at <datadir>/store/onbod.db (onbod owns + migrates this DB).
// Mirrors routd.Open / runed.Open: WAL, migrations first so the tables exist
// before any read/write.
func openOwnedDB(ownDSN string) (*sql.DB, error) {
	// Strict: onbod never creates onbod.db. sql.Open is lazy and SQLite creates
	// a missing file on first query, so a wrong path would migrate a fresh empty
	// file and drop every invite and gate while onbod reports healthy
	// (spec 5/16 step 7, BUGS F52). `arizuko create` seeds the file.
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

// migrateOwned builds onbod's OWN schema in db: the embedded chain, the entities
// the chain no longer creates, and the two Go steps a reshaping migration cannot
// do. BackfillInviteRefs and CarryOnboardingLegacy carry the invites_legacy /
// onboarding_legacy tables a pre-move database left behind and drop them
// (store/invites.go, store/onboarding.go); BackfillInviteRefs also hashes
// plaintext invites at rest, which SQL cannot do — SQLite has no sha256(). Both
// are no-ops on a database created since the move, which never grows either
// table.
//
// Split out of openOwnedDB so a test builds the schema onbod ships from the one
// declaration of it, rather than a second one that drifts.
func migrateOwned(db *sql.DB) error {
	if err := db_utils.Migrate(db, onbodMigrationFS, "migrations", onbodServiceName); err != nil {
		return err
	}
	// Every one of onbod's own tables is an entity rather than a migration, so
	// the driver creates them here — the chain above only records that it has
	// nothing left to build (onbod/migrations/0001).
	//
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
	if err := resreg.EnsureSubsystem(ctx, obj, obj, resreg.SubsystemOnbod); err != nil {
		return err
	}
	if err := store.BackfillInviteRefs(db); err != nil {
		return err
	}
	return store.CarryOnboardingLegacy(db)
}
