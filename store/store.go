package store

import (
	"database/sql"
	"embed"
	"os"
	"path/filepath"

	"github.com/onvos/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "store"

type Store struct {
	db *sql.DB
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "messages.db") + "?_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func OpenMem() (*Store, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

// Migrate applies store's embedded SQL migrations to the given DB.
// Exposed for daemons and tests that hold a raw *sql.DB but still
// need the canonical schema — gated (the main daemon) owns schema
// ownership, but tests for other daemons use this to set up fixtures.
func Migrate(db *sql.DB) error {
	return db_utils.Migrate(db, migrationFS, "migrations", serviceName)
}
