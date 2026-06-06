package store

import (
	"crypto/sha256"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "store"

type Store struct {
	db *sql.DB
	// secretKeys is the AES-256 keyring for the secrets table. keys[0] is the
	// active key used to seal new writes; the rest are decrypt-only, retained
	// so an operator can rotate the active key without losing access to values
	// sealed under an older one (set the new key first, old ones after, then a
	// startup re-seal migrates them). Empty = encryption disabled.
	secretKeys [][32]byte
}

// SetSecretKeys derives a 32-byte AES key from each raw value via SHA-256.
// raws[0] becomes the active (seal) key; the rest are decrypt-only retired
// keys for rotation. Call after Open. No raws = encryption disabled.
func (s *Store) SetSecretKeys(raws ...[]byte) {
	keys := make([][32]byte, 0, len(raws))
	for _, r := range raws {
		keys = append(keys, sha256.Sum256(r))
	}
	s.secretKeys = keys
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	// foreign_keys must ride the DSN: modernc.org/sqlite defaults FK
	// enforcement OFF per-connection, and database/sql pools many
	// connections — a one-shot PRAGMA only covers one of them, so
	// ON DELETE CASCADE (migrations 0068/0069) would silently no-op on
	// the others. DSN pragmas apply to every pooled connection.
	dsn := filepath.Join(dir, "messages.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
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

// OpenRoutd opens routd.db at dir/routd.db (WAL, FK on) and wraps it as a
// *Store WITHOUT running store's migrations — routd owns that file and already
// created its tables (acl/secrets/tasks/pane via routd's own migration set).
// The host-admin CLI (arizuko grant/secret/user-secret) writes acl + secrets
// here in the split topology instead of opening messages.db, since those tables
// now live in routd.db (spec 5/5 § Daemon ownership). Strict: errors if routd.db
// has no acl table (routd never booted to migrate it) rather than silently
// creating a divergent schema.
func OpenRoutd(dir string) (*Store, error) {
	dsn := filepath.Join(dir, "routd.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	var n int
	if err := db.QueryRow(
		"SELECT 1 FROM sqlite_master WHERE type='table' AND name='acl'").Scan(&n); err != nil {
		db.Close()
		return nil, fmt.Errorf("routd.db at %s has no acl table (routd must boot to migrate it first): %w", dir, err)
	}
	return &Store{db: db}, nil
}

func OpenMem() (*Store, error) {
	// `:memory:` SQLite is per-connection; database/sql can pool a second
	// connection that sees an empty DB. `cache=shared` makes the in-memory
	// DB shared across connections in the same process. File-backed Open()
	// doesn't have this constraint.
	db, err := sql.Open("sqlite", "file::memory:?cache=shared&_pragma=foreign_keys(on)")
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
