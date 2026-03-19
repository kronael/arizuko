package main

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"embed"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "timed"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	dataDir := os.Getenv("DATA_DIR")
	groupsDir := filepath.Join(dataDir, "groups")

	dsn := os.Getenv("DATABASE")
	if dsn == "" {
		if dataDir == "" {
			slog.Error("DATABASE or DATA_DIR env required")
			os.Exit(1)
		}
		dsn = filepath.Join(dataDir, "store", "messages.db")
	}
	tz := os.Getenv("TZ")
	if _, err := time.LoadLocation(tz); tz == "" || err != nil {
		tz = "UTC"
	}
	db, err := sql.Open("sqlite", dsn+"?_busy_timeout=5000")
	if err != nil {
		slog.Error("open db", "err", err)
		os.Exit(1)
	}
	defer db.Close()
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		slog.Warn("set WAL mode", "err", err)
	}

	if err := migrate(db); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	slog.Info("scheduler started", "db", dsn, "tz", tz)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()

	daily := time.NewTicker(24 * time.Hour)
	defer daily.Stop()

	fire(db, tz) // first tick immediately
	for {
		select {
		case <-tick.C:
			fire(db, tz)
		case <-daily.C:
			cleanupSpawns(db, groupsDir)
		case <-stop:
			slog.Info("scheduler stopped")
			return
		}
	}
}

func fire(db *sql.DB, tz string) {
	rows, err := db.Query(
		`SELECT id, chat_jid, prompt, cron FROM scheduled_tasks
		 WHERE status = 'active' AND next_run <= ?`,
		time.Now().Format(time.RFC3339))
	if err != nil {
		slog.Error("query due tasks", "err", err)
		return
	}
	defer rows.Close()

	type task struct {
		id, jid, prompt string
		cronExpr        *string
	}
	var due []task
	for rows.Next() {
		var t task
		rows.Scan(&t.id, &t.jid, &t.prompt, &t.cronExpr)
		due = append(due, t)
	}

	for _, t := range due {
		id := fmt.Sprintf("sched-%s-%d", t.id, time.Now().UnixNano())
		_, err := db.Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
			 VALUES (?, ?, 'scheduler', ?, ?)`,
			id, t.jid, t.prompt, time.Now().Format(time.RFC3339))
		if err != nil {
			slog.Error("insert message", "task", t.id, "err", err)
			continue
		}

		if t.cronExpr != nil && *t.cronExpr != "" {
			next, err := nextCron(*t.cronExpr, tz)
			if err == nil {
				if _, err := db.Exec(`UPDATE scheduled_tasks SET next_run = ? WHERE id = ?`,
					next.Format(time.RFC3339), t.id); err != nil {
					slog.Warn("update task next_run", "task", t.id, "err", err)
				}
			}
		} else {
			if _, err := db.Exec(
				`UPDATE scheduled_tasks SET status = 'completed', next_run = NULL WHERE id = ?`, t.id,
			); err != nil {
				slog.Warn("complete task", "task", t.id, "err", err)
			}
		}

		slog.Info("fired task", "id", t.id, "jid", t.jid)
	}
}

func nextCron(expr, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	s, err := p.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(time.Now().In(loc)), nil
}

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS migrations (
		service TEXT NOT NULL, version INTEGER NOT NULL, applied_at TEXT NOT NULL,
		PRIMARY KEY (service, version))`); err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	var max int
	if err := db.QueryRow("SELECT COALESCE(MAX(version),0) FROM migrations WHERE service=?",
		serviceName).Scan(&max); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}

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
		if err := runMigration(db, f, ver); err != nil {
			return err
		}
		max = ver
	}
	return nil
}

func runMigration(db *sql.DB, f string, ver int) error {
	raw, err := migrationFS.ReadFile("migrations/" + f)
	if err != nil {
		return fmt.Errorf("%s: read: %w", f, err)
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(string(raw)); err != nil {
		return fmt.Errorf("%s: %w", f, err)
	}
	if _, err := tx.Exec(
		"INSERT INTO migrations (service, version, applied_at) VALUES (?,?,?)",
		serviceName, ver, time.Now().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("%s: record: %w", f, err)
	}
	return tx.Commit()
}

func cleanupSpawns(db *sql.DB, groupsDir string) {
	// 1. Close active spawns with no recent messages past their TTL
	rows, err := db.Query(
		`SELECT g.folder, g.spawn_ttl_days
		 FROM registered_groups g
		 WHERE g.parent IS NOT NULL AND g.parent != '' AND g.state = 'active'`)
	if err != nil {
		return
	}
	defer rows.Close()
	now := time.Now()
	for rows.Next() {
		var folder string
		var ttlDays int
		rows.Scan(&folder, &ttlDays)
		var lastMsg string
		db.QueryRow(`SELECT MAX(timestamp) FROM messages WHERE chat_jid IN (
			SELECT jid FROM registered_groups WHERE folder = ?)`, folder).Scan(&lastMsg)
		if lastMsg == "" {
			continue // never had messages, don't close
		}
		t, err := time.Parse(time.RFC3339, lastMsg)
		if err != nil {
			continue
		}
		if now.Sub(t) > time.Duration(ttlDays)*24*time.Hour {
			db.Exec(`UPDATE registered_groups SET state='closed', updated_at=? WHERE folder=?`,
				now.Format(time.RFC3339), folder)
			slog.Info("closed idle spawn", "folder", folder)
		}
	}

	// 2. Archive closed groups past their archive_closed_days threshold
	rows2, err := db.Query(
		`SELECT folder, parent, archive_closed_days, updated_at
		 FROM registered_groups WHERE state = 'closed'`)
	if err != nil {
		return
	}
	defer rows2.Close()
	type archiveTarget struct{ folder, parent string }
	var toArchive []archiveTarget
	for rows2.Next() {
		var folder, parent, updatedAt string
		var archiveDays int
		rows2.Scan(&folder, &parent, &archiveDays, &updatedAt)
		t, err := time.Parse(time.RFC3339, updatedAt)
		if err != nil {
			continue
		}
		if now.Sub(t) > time.Duration(archiveDays)*24*time.Hour {
			toArchive = append(toArchive, archiveTarget{folder, parent})
		}
	}
	for _, g := range toArchive {
		archiveSpawn(db, groupsDir, g.folder, g.parent)
	}
}

func archiveSpawn(db *sql.DB, groupsDir, folder, parent string) {
	leaf := filepath.Base(folder)
	archiveDir := filepath.Join(groupsDir, parent, "archive")
	os.MkdirAll(archiveDir, 0o755)
	archivePath := filepath.Join(archiveDir, fmt.Sprintf("%s-%d.tar.gz", leaf, time.Now().Unix()))

	srcDir := filepath.Join(groupsDir, folder)
	if err := compressDirTarGz(srcDir, archivePath); err != nil {
		slog.Error("archive spawn", "folder", folder, "err", err)
		return
	}
	db.Exec(`DELETE FROM registered_groups WHERE folder = ?`, folder)
	os.RemoveAll(srcDir)
	slog.Info("archived spawn", "folder", folder, "archive", archivePath)
}

func compressDirTarGz(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return err
		}
		rel, _ := filepath.Rel(src, path)
		info, _ := d.Info()
		hdr, _ := tar.FileInfoHeader(info, "")
		hdr.Name = rel
		tw.WriteHeader(hdr)
		if !d.IsDir() {
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()
			io.Copy(tw, file)
		}
		return nil
	})
}
