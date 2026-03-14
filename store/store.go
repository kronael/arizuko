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

func (s *Store) migrate() error {
	s.db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		version INTEGER PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`)

	s.seedFromPragma()

	var maxVer int
	s.db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM migrations").Scan(&maxVer)

	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		ver, err := strconv.Atoi(name[:4])
		if err != nil {
			continue
		}
		if ver <= maxVer {
			continue
		}
		if ver != maxVer+1 {
			return fmt.Errorf("migration gap: expected %d, found %d", maxVer+1, ver)
		}

		sql, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(sql)); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		tx.Exec("INSERT INTO migrations (version, applied_at) VALUES (?, ?)",
			ver, time.Now().Format(time.RFC3339))
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}

		maxVer = ver
	}

	return nil
}

// seedFromPragma bridges old PRAGMA user_version databases to the
// new migrations table. Maps old versions to new migration numbers
// and marks them as already applied.
func (s *Store) seedFromPragma() {
	var n int
	s.db.QueryRow("SELECT COUNT(*) FROM migrations").Scan(&n)
	if n > 0 {
		return
	}

	var ver int
	s.db.QueryRow("PRAGMA user_version").Scan(&ver)
	if ver == 0 {
		return
	}

	// Old PRAGMA versions map to new migration numbers:
	// pragma 1 (jid format)        → migrations 1,2
	// pragma 2 (task reported)     → migrations 1,2,3
	// pragma 3 (flat routing)      → migrations 1,2,3,4
	// pragma 4 (reply_to_id)       → migrations 1,2,3,4,5
	// pragma 5 (agent_cursor)      → migrations 1,2,3,4,5,6
	maxMig := ver + 1
	if maxMig > 6 {
		maxMig = 6
	}

	now := time.Now().Format(time.RFC3339)
	for i := 1; i <= maxMig; i++ {
		s.db.Exec("INSERT OR IGNORE INTO migrations (version, applied_at) VALUES (?, ?)",
			i, now)
	}
}

func nullStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
