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
	"strconv"
	"syscall"
	"time"

	"github.com/onvos/arizuko/dbmig"
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

	if err := dbmig.Run(db, migrationFS, "migrations", serviceName); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	slog.Info("scheduler started", "db", dsn, "tz", tz)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

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

func isIntervalMs(cron string) (int64, bool) {
	v, err := strconv.ParseInt(cron, 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

func logRun(db *sql.DB, taskID, status, errText string, durationMs int64) {
	var errVal *string
	if errText != "" {
		errVal = &errText
	}
	db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, error)
		 VALUES (?, ?, ?, ?, ?)`,
		taskID, time.Now().Format(time.RFC3339), durationMs, status, errVal)
}

func fire(db *sql.DB, tz string) {
	now := time.Now()
	slog.Debug("poll tasks", "at", now.Format(time.RFC3339))

	// Atomic claim: mark due tasks as 'firing' so concurrent polls skip them.
	if _, err := db.Exec(
		`UPDATE scheduled_tasks SET status = 'firing'
		 WHERE status = 'active' AND next_run <= ?`,
		now.Format(time.RFC3339)); err != nil {
		slog.Error("claim due tasks", "err", err)
		return
	}

	rows, err := db.Query(
		`SELECT id, chat_jid, prompt, cron, context_mode FROM scheduled_tasks
		 WHERE status = 'firing'`)
	if err != nil {
		slog.Error("query claimed tasks", "err", err)
		return
	}
	defer rows.Close()

	type task struct {
		id, jid, prompt, contextMode string
		cronExpr                     *string
	}
	var due []task
	for rows.Next() {
		var t task
		rows.Scan(&t.id, &t.jid, &t.prompt, &t.cronExpr, &t.contextMode)
		due = append(due, t)
	}

	slog.Debug("due tasks", "count", len(due))

	for _, t := range due {
		start := time.Now()
		sender := "timed"
		if t.contextMode == "isolated" {
			sender = "timed-isolated:" + t.id
		}
		id := fmt.Sprintf("sched-%s-%d", t.id, start.UnixNano())
		_, err := db.Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
			 VALUES (?, ?, ?, ?, ?)`,
			id, t.jid, sender, t.prompt, start.Format(time.RFC3339))
		if err != nil {
			slog.Error("insert message", "task", t.id, "err", err)
			logRun(db, t.id, "error", err.Error(), time.Since(start).Milliseconds())
			// Restore to active so it can be retried.
			db.Exec(`UPDATE scheduled_tasks SET status = 'active' WHERE id = ?`, t.id)
			continue
		}

		cronVal := ""
		if t.cronExpr != nil {
			cronVal = *t.cronExpr
		}

		var nextRun string
		if ms, ok := isIntervalMs(cronVal); ok {
			nextRun = time.Now().Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339)
		} else if cronVal != "" {
			next, err := nextCron(cronVal, tz)
			if err != nil {
				slog.Warn("parse cron expr", "task", t.id, "cron", cronVal, "err", err)
			} else {
				nextRun = next.Format(time.RFC3339)
			}
		}

		if nextRun != "" {
			if _, err := db.Exec(
				`UPDATE scheduled_tasks SET status = 'active', next_run = ? WHERE id = ?`,
				nextRun, t.id); err != nil {
				slog.Warn("update task next_run", "task", t.id, "err", err)
			}
		} else if cronVal == "" {
			if _, err := db.Exec(
				`UPDATE scheduled_tasks SET status = 'completed', next_run = NULL WHERE id = ?`,
				t.id); err != nil {
				slog.Warn("complete task", "task", t.id, "err", err)
			}
		} else {
			db.Exec(`UPDATE scheduled_tasks SET status = 'active' WHERE id = ?`, t.id)
		}

		logRun(db, t.id, "success", "", time.Since(start).Milliseconds())
		slog.Info("fired task",
			"id", t.id, "jid", t.jid, "cron", cronVal,
			"context_mode", t.contextMode, "next_run", nextRun)
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

func cleanupSpawns(db *sql.DB, groupsDir string) {
	rows, err := db.Query(
		`SELECT g.folder, g.spawn_ttl_days
		 FROM groups g
		 WHERE g.parent IS NOT NULL AND g.parent != '' AND g.state = 'active'`)
	if err != nil {
		return
	}
	defer rows.Close()
	now := time.Now()
	checkedSpawns := 0
	for rows.Next() {
		checkedSpawns++
		var folder string
		var ttlDays int
		rows.Scan(&folder, &ttlDays)
		var lastMsg string
		db.QueryRow(`SELECT MAX(timestamp) FROM messages WHERE chat_jid IN (
			SELECT jid FROM routes WHERE target = ? AND type = 'default')`, folder).Scan(&lastMsg)
		if lastMsg == "" {
			continue // never had messages, don't close
		}
		t, err := time.Parse(time.RFC3339, lastMsg)
		if err != nil {
			continue
		}
		if now.Sub(t) > time.Duration(ttlDays)*24*time.Hour {
			db.Exec(`UPDATE groups SET state='closed', updated_at=? WHERE folder=?`,
				now.Format(time.RFC3339), folder)
			slog.Info("closed idle spawn", "folder", folder)
		}
	}
	slog.Debug("cleanup spawns: checked active", "count", checkedSpawns)

	rows2, err := db.Query(
		`SELECT folder, parent, archive_closed_days, updated_at
		 FROM groups WHERE state = 'closed'`)
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
	if len(toArchive) == 0 {
		slog.Debug("cleanup spawns: nothing to archive")
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
	db.Exec(`DELETE FROM groups WHERE folder = ?`, folder)
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
