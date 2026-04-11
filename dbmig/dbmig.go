package dbmig

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Run applies pending SQL migrations from an embedded filesystem.
// Each migration file must have a 4-digit version prefix (e.g. 0001-init.sql).
// Versions must be sequential with no gaps.
func Run(db *sql.DB, fsys embed.FS, dir, service string) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var max int
	if err := db.QueryRow("SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
		service).Scan(&max); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

	entries, _ := fsys.ReadDir(dir)
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
		raw, err := fsys.ReadFile(dir + "/" + f)
		if err != nil {
			return fmt.Errorf("%s: read: %w", f, err)
		}
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		stmts := strings.Split(string(raw), ";")
		for _, stmt := range stmts {
			stmt = strings.TrimSpace(stmt)
			if stmt == "" {
				continue
			}
			if _, err := tx.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
		if _, err := tx.Exec(
			"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
			service, ver, time.Now().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("%s: record: %w", f, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("%s: commit: %w", f, err)
		}
		max = ver
	}
	return nil
}
