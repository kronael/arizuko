package db_utils

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
