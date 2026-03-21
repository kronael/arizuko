package main

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type emailThread struct {
	ThreadID    string
	FromAddress string
	RootMsgID   string
}

func openDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "emaid.db"))
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS email_threads (
			thread_id TEXT PRIMARY KEY,
			from_address TEXT NOT NULL,
			root_msg_id TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS email_msg_ids (
			msg_id TEXT PRIMARY KEY,
			thread_id TEXT NOT NULL
		);
	`)
	return db, err
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

func storeThread(db *sql.DB, msgID, threadID, fromAddress, rootMsgID string) {
	db.Exec(`INSERT OR IGNORE INTO email_threads (thread_id, from_address, root_msg_id) VALUES (?,?,?)`,
		threadID, fromAddress, rootMsgID)
	db.Exec(`INSERT OR IGNORE INTO email_msg_ids (msg_id, thread_id) VALUES (?,?)`,
		msgID, threadID)
}
