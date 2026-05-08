package main

import (
	"database/sql"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/robfig/cron/v3"
	_ "modernc.org/sqlite"
)

var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	dataDir := os.Getenv("DATA_DIR")

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

	slog.Info("scheduler started", "db", dsn, "tz", tz)

	go func() {
		mux := http.NewServeMux()
		mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
			if err := db.Ping(); err != nil {
				http.Error(w, "db unreachable", http.StatusServiceUnavailable)
				return
			}
			_, _ = w.Write([]byte("ok"))
		})
		if err := http.ListenAndServe(":8080", mux); err != nil {
			slog.Error("health server", "err", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	tick := time.NewTicker(60 * time.Second)
	defer tick.Stop()

	fire(db, tz)
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

func logRun(db *sql.DB, taskID, status, errText string, durationMs int64) {
	db.Exec(
		`INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, error)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''))`,
		taskID, time.Now().Format(time.RFC3339), durationMs, status, errText)
}

func fire(db *sql.DB, tz string) {
	now := time.Now()

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
		id, jid, prompt, cronExpr, contextMode string
	}
	var due []task
	for rows.Next() {
		var t task
		var cronExpr sql.NullString
		rows.Scan(&t.id, &t.jid, &t.prompt, &cronExpr, &t.contextMode)
		t.cronExpr = cronExpr.String
		due = append(due, t)
	}

	for _, t := range due {
		start := time.Now()
		sender := "timed"
		if t.contextMode == "isolated" {
			sender = "timed-isolated:" + t.id
		}
		id := core.MsgID("sched-" + t.id)
		_, err := db.Exec(
			`INSERT INTO messages (id, chat_jid, sender, content, timestamp)
			 VALUES (?, ?, ?, ?, ?)`,
			id, t.jid, sender, t.prompt, start.Format(time.RFC3339))
		if err != nil {
			slog.Error("insert message", "task", t.id, "jid", t.jid, "err", err)
			logRun(db, t.id, "error", err.Error(), time.Since(start).Milliseconds())
			db.Exec(`UPDATE scheduled_tasks SET status = 'active' WHERE id = ?`, t.id)
			continue
		}

		nextRun := computeNextRun(t.cronExpr, tz, t.id)
		switch {
		case nextRun != "":
			db.Exec(`UPDATE scheduled_tasks SET status = 'active', next_run = ? WHERE id = ?`,
				nextRun, t.id)
		case t.cronExpr == "":
			db.Exec(`UPDATE scheduled_tasks SET status = 'completed', next_run = NULL WHERE id = ?`,
				t.id)
		default:
			db.Exec(`UPDATE scheduled_tasks SET status = 'active' WHERE id = ?`, t.id)
		}

		logRun(db, t.id, "success", "", time.Since(start).Milliseconds())
		slog.Info("fired task",
			"id", t.id, "jid", t.jid, "cron", t.cronExpr,
			"context_mode", t.contextMode, "next_run", nextRun)
	}
}

func computeNextRun(cronVal, tz, taskID string) string {
	if ms, err := strconv.ParseInt(cronVal, 10, 64); err == nil && ms > 0 {
		return time.Now().Add(time.Duration(ms) * time.Millisecond).Format(time.RFC3339)
	}
	if cronVal == "" {
		return ""
	}
	next, err := nextCron(cronVal, tz)
	if err != nil {
		slog.Warn("parse cron expr", "task", taskID, "cron", cronVal, "err", err)
		return ""
	}
	return next.Format(time.RFC3339)
}

func nextCron(expr, tz string) (time.Time, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	s, err := cronParser.Parse(expr)
	if err != nil {
		return time.Time{}, err
	}
	return s.Next(time.Now().In(loc)), nil
}
