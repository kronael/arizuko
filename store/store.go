package store

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

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
	if err := s.migrate(); err != nil {
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
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

const serviceName = "store"

func (s *Store) migrate() error {
	s.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`)
	s.seedFromPragma()

	var max int
	s.db.QueryRow("SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
		serviceName).Scan(&max)

	entries, _ := migrationFS.ReadDir("migrations")
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		ver, _ := strconv.Atoi(f[:4])
		if ver <= max {
			continue
		}
		if ver != max+1 {
			return fmt.Errorf("migration gap: expected %d, got %d", max+1, ver)
		}
		if err := s.runMigration(f, ver); err != nil {
			return err
		}
		max = ver
	}
	return nil
}

func (s *Store) runMigration(f string, ver int) error {
	raw, err := migrationFS.ReadFile("migrations/" + f)
	if err != nil {
		return fmt.Errorf("%s: read: %w", f, err)
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(raw)); err != nil {
		return fmt.Errorf("%s: %w", f, err)
	}
	if _, err := tx.Exec("INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
		serviceName, ver, time.Now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: record: %w", f, err)
	}
	return tx.Commit()
}

func (s *Store) seedFromPragma() {
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM migrations WHERE service=?", serviceName).Scan(&n)
	if n > 0 {
		return
	}
	var ver int
	s.db.QueryRow("PRAGMA user_version").Scan(&ver)
	if ver == 0 {
		return
	}
	m := map[int]int{1: 2, 2: 3, 3: 3, 4: 4, 5: 5}
	maxMig := m[ver]
	if maxMig == 0 {
		maxMig = 5
	}
	now := time.Now().Format(time.RFC3339)
	for i := 1; i <= maxMig; i++ {
		s.db.Exec("INSERT OR IGNORE INTO migrations (service,version,applied_at) VALUES (?,?,?)",
			serviceName, i, now)
	}
}
