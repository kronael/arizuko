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
	// SQLite has no sha256(), so migration 0003 (invites hash-at-rest) only
	// reshapes the table; this carries any pre-existing plaintext rows forward
	// and drops the renamed-out-of-the-way invites_legacy (store/invites.go).
	if err := store.BackfillInviteRefs(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}
