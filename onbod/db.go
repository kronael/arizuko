package main

import (
	"database/sql"
	"embed"

	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

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
	if err := db_utils.RequireDBFile(ownDSN); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", ownDSN+"?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := db_utils.Migrate(db, onbodMigrationFS, "migrations", onbodServiceName); err != nil {
		db.Close()
		return nil, err
	}
	// SQLite has no sha256(), so migrations 0003 (invites) and 0004
	// (onboarding) only reshape their tables; these carry any pre-existing
	// plaintext rows forward and drop the renamed-out-of-the-way
	// invites_legacy / onboarding_legacy (store/invites.go, store/onboarding.go).
	if err := store.BackfillInviteRefs(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := store.BackfillOnboardingTokenRefs(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
