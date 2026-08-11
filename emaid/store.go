package main

import (
	"database/sql"
	"embed"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/db_utils"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

const serviceName = "emaid"

type emailThread struct {
	ThreadID    string
	FromAddress string
	RootMsgID   string
}

// openDB opens <dataDir>/emaid.db with the owner-DB conventions (WAL,
// busy_timeout, foreign keys on every pooled connection) and applies the
// emaid migrations. emaid creates a missing file itself: emaid.db is
// adapter-private state, and `arizuko create` does not seed it.
func openDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dataDir, "emaid.db") + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func getThreadByMsgID(db *sql.DB, msgID string) *emailThread {
	var t emailThread
	row := db.QueryRow(`
		SELECT et.thread_id, et.from_address, et.root_msg_id
		FROM email_msg_ids mi
		JOIN email_threads et ON et.thread_id = mi.thread_id
		WHERE mi.msg_id = ?`, msgID)
	if err := row.Scan(&t.ThreadID, &t.FromAddress, &t.RootMsgID); err != nil {
		return nil
	}
	return &t
}

// upsertThread atomically records a message→thread mapping.
// Concurrent polls on the same message ID are safe (INSERT OR IGNORE is idempotent).
func upsertThread(db *sql.DB, msgID, threadID, fromAddress, rootMsgID string) {
	tx, err := db.Begin()
	if err != nil {
		slog.Error("upsertThread begin", "thread", threadID, "err", err)
		return
	}
	defer tx.Rollback()
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO email_threads (thread_id, from_address, root_msg_id) VALUES (?,?,?)`,
		threadID, fromAddress, rootMsgID); err != nil {
		slog.Error("upsertThread email_threads", "thread", threadID, "err", err)
		return
	}
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO email_msg_ids (msg_id, thread_id) VALUES (?,?)`,
		msgID, threadID); err != nil {
		slog.Error("upsertThread email_msg_ids", "msg", msgID, "err", err)
		return
	}
	if err := tx.Commit(); err != nil {
		slog.Error("upsertThread commit", "thread", threadID, "err", err)
	}
}
