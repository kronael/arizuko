package db_utils

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// RequireDBFile errors unless path already holds a SQLite file. Owner daemons
// call it before sql.Open: SQLite creates a missing file silently and Migrate
// then fills it with a complete empty schema, so a daemon pointed at the wrong
// path — a typo'd mount, an unfinished store/<owner>/ move — boots green as a
// fresh instance holding none of the operator's data (spec 5/16 step 7).
// Creation is an explicit act: CreateDBFile, called by `arizuko create`.
func RequireDBFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%s does not exist: an owner daemon never creates its own "+
				"database (run `arizuko create`, or move the existing .db + -wal + -shm here)", path)
		}
		return fmt.Errorf("stat %s: %w", path, err)
	}
	return nil
}

// CreateDBFile makes path's parent directory and an empty file at path when
// absent, leaving an existing file untouched. A zero-byte file IS a valid empty
// SQLite database, so the owner daemon's own Migrate fills in the schema on
// first boot — this seeds the file without linking every migration set into the
// CLI. `arizuko create` and the split-cutover tool only.
func CreateDBFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	return f.Close()
}

// Migrate applies pending `NNNN-*.sql` migrations from fsys/dir. Versions
// must be sequential with no gaps. Tracked in the `migrations` table
// keyed on (service, version).
func Migrate(db *sql.DB, fsys embed.FS, dir, service string) error {
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

	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read migrations dir %q: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)

	for _, f := range files {
		if len(f) < 4 {
			return fmt.Errorf("migration %q: filename too short for 4-digit version", f)
		}
		ver, err := strconv.Atoi(f[:4])
		if err != nil {
			return fmt.Errorf("migration %q: non-numeric version prefix: %w", f, err)
		}
		if ver <= max {
			continue
		}
		raw, err := fsys.ReadFile(dir + "/" + f)
		if err != nil {
			return fmt.Errorf("%s: read: %w", f, err)
		}
		// Serialise concurrent openers of the same DB (e.g. webd + proxyd
		// both racing to apply the same migration). BEGIN EXCLUSIVE holds a
		// write lock for the duration, so the second opener blocks until the
		// first commits, then sees cur >= ver and skips.
		ctx := context.Background()
		conn, err := db.Conn(ctx)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
			conn.Close()
			return fmt.Errorf("%s: exclusive lock: %w", f, err)
		}
		var cur int
		if err := conn.QueryRowContext(ctx,
			"SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
			service).Scan(&cur); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return fmt.Errorf("%s: recheck version: %w", f, err)
		}
		if cur >= ver {
			// another process applied this migration while we were waiting
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			max = cur
			continue
		}
		if ver != cur+1 {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return fmt.Errorf("migration gap: expected %d, got %d", cur+1, ver)
		}
		if _, err := conn.ExecContext(ctx, string(raw)); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return fmt.Errorf("%s: %w", f, err)
		}
		if _, err := conn.ExecContext(ctx,
			"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
			service, ver, time.Now().Format(time.RFC3339)); err != nil {
			_, _ = conn.ExecContext(ctx, "ROLLBACK")
			conn.Close()
			return fmt.Errorf("%s: record: %w", f, err)
		}
		if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
			conn.Close()
			return fmt.Errorf("%s: commit: %w", f, err)
		}
		conn.Close()
		max = ver
	}
	return nil
}
