package main

import (
	"database/sql"
	"embed"
	"fmt"
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

	dsn := os.Getenv("DATABASE")
	if dsn == "" {
		dataDir := os.Getenv("DATA_DIR")
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

	fire(db, tz) // first tick immediately
	for {
		select {
		case <-tick.C:
			fire(db, tz)
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
