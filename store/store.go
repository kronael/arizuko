package store

import (
	"database/sql"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}
	dsn := filepath.Join(dir, "messages.db") + "?_busy_timeout=5000"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func OpenMem() (*Store, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	for _, stmt := range schema {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	var ver int
	s.db.QueryRow("PRAGMA user_version").Scan(&ver)
	if ver < 1 {
		s.migrateV1()
		s.db.Exec("PRAGMA user_version = 1")
		ver = 1
	}
	if ver < 2 {
		s.migrateV2()
		s.db.Exec("PRAGMA user_version = 2")
	}
	return nil
}

func (s *Store) migrateV2() {
	s.db.Exec(`ALTER TABLE task_run_logs ADD COLUMN reported INTEGER DEFAULT 0`)
}

func (s *Store) migrateV1() {
	s.db.Exec(`UPDATE chats SET jid = 'telegram:' || jid WHERE jid GLOB '[0-9]*' AND channel = 'telegram'`)
	s.db.Exec(`UPDATE messages SET chat_jid = 'telegram:' || chat_jid WHERE chat_jid GLOB '[0-9]*' AND EXISTS (SELECT 1 FROM chats WHERE chats.jid = 'telegram:' || messages.chat_jid AND chats.channel = 'telegram')`)
	s.db.Exec(`UPDATE registered_groups SET jid = 'telegram:' || jid WHERE jid GLOB '[0-9]*'`)
	s.db.Exec(`UPDATE chats SET jid = 'whatsapp:' || jid WHERE jid NOT LIKE '%:%' AND channel = 'whatsapp'`)
	s.db.Exec(`UPDATE chats SET jid = 'discord:' || jid WHERE jid NOT LIKE '%:%' AND channel = 'discord'`)
}

var schema = []string{
	`CREATE TABLE IF NOT EXISTS chats (
		jid TEXT PRIMARY KEY,
		name TEXT,
		channel TEXT,
		is_group INTEGER DEFAULT 0,
		last_message_time TEXT,
		errored INTEGER DEFAULT 0
	)`,

	`CREATE TABLE IF NOT EXISTS messages (
		id TEXT PRIMARY KEY,
		chat_jid TEXT NOT NULL,
		sender TEXT NOT NULL,
		sender_name TEXT,
		content TEXT NOT NULL,
		timestamp TEXT NOT NULL,
		is_from_me INTEGER DEFAULT 0,
		is_bot_message INTEGER DEFAULT 0,
		forwarded_from TEXT,
		reply_to_text TEXT,
		reply_to_sender TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_chat_ts ON messages(chat_jid, timestamp)`,

	`CREATE TABLE IF NOT EXISTS registered_groups (
		jid TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		folder TEXT NOT NULL,
		trigger_word TEXT NOT NULL,
		added_at TEXT NOT NULL,
		container_config TEXT,
		requires_trigger INTEGER DEFAULT 1,
		slink_token TEXT,
		parent TEXT,
		routing_rules TEXT
	)`,

	`CREATE TABLE IF NOT EXISTS sessions (
		group_folder TEXT PRIMARY KEY,
		session_id TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS session_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_folder TEXT NOT NULL,
		session_id TEXT NOT NULL,
		started_at TEXT NOT NULL,
		ended_at TEXT,
		result TEXT,
		error TEXT,
		message_count INTEGER
	)`,

	`CREATE TABLE IF NOT EXISTS system_messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		group_id TEXT NOT NULL,
		origin TEXT NOT NULL,
		event TEXT NOT NULL,
		attrs TEXT,
		body TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS scheduled_tasks (
		id TEXT PRIMARY KEY,
		group_folder TEXT NOT NULL,
		chat_jid TEXT NOT NULL,
		prompt TEXT NOT NULL,
		schedule_type TEXT NOT NULL,
		schedule_value TEXT NOT NULL,
		context_mode TEXT NOT NULL DEFAULT 'group',
		next_run TEXT,
		last_run TEXT,
		last_result TEXT,
		status TEXT NOT NULL DEFAULT 'active',
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS task_run_logs (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		task_id TEXT NOT NULL,
		run_at TEXT NOT NULL,
		duration_ms INTEGER NOT NULL,
		status TEXT NOT NULL,
		result TEXT,
		error TEXT
	)`,

	`CREATE TABLE IF NOT EXISTS router_state (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS auth_users (
		id INTEGER PRIMARY KEY,
		sub TEXT UNIQUE NOT NULL,
		username TEXT UNIQUE NOT NULL,
		hash TEXT NOT NULL,
		name TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS auth_sessions (
		token_hash TEXT PRIMARY KEY,
		user_sub TEXT NOT NULL,
		expires_at TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`,

	`CREATE TABLE IF NOT EXISTS email_threads (
		thread_id TEXT PRIMARY KEY,
		chat_jid TEXT NOT NULL,
		subject TEXT,
		last_message_id TEXT
	)`,
}
