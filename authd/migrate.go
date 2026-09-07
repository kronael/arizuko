package main

// migrate.go runs auth.db's schema. The SQL lives in authd/migrations/*.sql and
// the runner is shared, so this file holds no statement of its own; it stays in
// the daemon because db_utils.Migrate runs before authd has a store to run it
// on, and go:embed cannot reach the directory from a subpackage.

import (
	"database/sql"
	"embed"

	"github.com/kronael/arizuko/db_utils"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func migrate(db *sql.DB) error {
	return db_utils.Migrate(db, migrationFS, "migrations", "authd")
}
