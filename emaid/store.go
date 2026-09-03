package main

import (
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/kronael/arizuko/chanlib"
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
	db, err := db_utils.OpenWAL(db_utils.FileDSN(filepath.Join(dataDir, "emaid.db")))
	if err != nil {
		return nil, err
	}
	if err := db_utils.Migrate(db, migrationFS, "migrations", serviceName); err != nil {
		db.Close()
		return nil, err
	}
	// chanlib's outbound_dedup lands in the SAME file: db_utils.Migrate keys on
	// (service, version), so the two chains coexist and emaid keeps one DB.
	if err := chanlib.MigrateAdapterDB(db); err != nil {
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
//
// Every failure is returned, never just logged: without the email_msg_ids row
// the next inbound reply misses in getThreadByMsgID and opens a SECOND thread,
// so the user sees one conversation split in two. The caller aborts the message
// instead, leaving it unseen for the next poll to retry.
func upsertThread(db *sql.DB, msgID, threadID, fromAddress, rootMsgID string) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin thread %s: %w", threadID, err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO email_threads (thread_id, from_address, root_msg_id) VALUES (?,?,?)`,
		threadID, fromAddress, rootMsgID); err != nil {
		return fmt.Errorf("insert email_threads %s: %w", threadID, err)
	}
	if _, err = tx.Exec(
		`INSERT OR IGNORE INTO email_msg_ids (msg_id, thread_id) VALUES (?,?)`,
		msgID, threadID); err != nil {
		return fmt.Errorf("insert email_msg_ids %s: %w", msgID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit thread %s: %w", threadID, err)
	}
	return nil
}
