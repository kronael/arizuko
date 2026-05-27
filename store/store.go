package store

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "store"

type Store struct {
	db        *sql.DB
	secretKey *[32]byte
}

// SetSecretKey derives a 32-byte AES key from raw via SHA-256 and stores it
// for encrypt/decrypt of secrets table values. Call after Open.
func (s *Store) SetSecretKey(raw []byte) {
	h := sha256.Sum256(raw)
	s.secretKey = &h
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "messages.db") + "?_pragma=busy_timeout(5000)"
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
	// `:memory:` SQLite is per-connection. database/sql's pool can give a
	// fresh connection that sees an empty in-memory DB. Pin to one
	// connection so tests see consistent state across helper calls.
	// File-backed Open() does not have this constraint.
	db.SetMaxOpenConns(1)
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

// Migrate applies store's embedded SQL migrations to db. Used by test
// fixtures in other packages that open a raw *sql.DB.
func Migrate(db *sql.DB) error {
	return db_utils.Migrate(db, migrationFS, "migrations", serviceName)
}

// New wraps an already-open *sql.DB as a *Store. Caller owns the db lifetime
// and must have run migrations.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying *sql.DB for callers that need raw query access
// (e.g. audit poll goroutine). The caller must not close it.
func (s *Store) DB() *sql.DB { return s.db }

// sqlPH returns n comma-separated `?` placeholders for use in SQL IN (...).
// e.g. sqlPH(3) == "?,?,?"
func sqlPH(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, n*2-1)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}
