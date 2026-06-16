// seed: prepare a throwaway DATA_DIR for the playwright dashd suite.
//
// Creates the dir layout, runs migrations on routd.db (routd.Open) and
// messages.db (store.Open), and seeds:
//   - group `inbox` in routd.db
//   - route targeting it in routd.db
//   - ACL grant: testadmin → admin on ** (operator); testuser has none
//   - sample message in routd.db for /dash/activity
//   - MEMORY.md on disk for /dash/memory
//
// Since the split, dashd's adminDB() points at routd.db for all admin
// tables (acl/groups/routes/secrets). Seed must write to routd.db.
//
// Usage: seed -data <dir>
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

func main() {
	var dataDir string
	flag.StringVar(&dataDir, "data", "", "DATA_DIR to seed (required)")
	flag.Parse()
	if dataDir == "" {
		log.Fatal("seed: -data required")
	}

	storeDir := filepath.Join(dataDir, "store")
	for _, sub := range []string{storeDir, filepath.Join(dataDir, "groups"), filepath.Join(dataDir, "groups", "inbox")} {
		if err := os.MkdirAll(sub, 0o755); err != nil {
			log.Fatalf("seed: mkdir %s: %v", sub, err)
		}
	}

	// routd.db — admin tables (acl, groups, routes, secrets, messages).
	rdb, err := routd.Open(storeDir)
	if err != nil {
		log.Fatalf("seed: open routd.db: %v", err)
	}
	defer rdb.Close()

	// messages.db — apply schema so dashd opens it without errors.
	sdb, err := store.Open(storeDir)
	if err != nil {
		log.Fatalf("seed: open messages.db: %v", err)
	}
	defer sdb.Close()

	// onbod.db — needed for /dash/invites/. dashd opens it as dbOnbod via
	// store.OpenOnbod which requires the invites table to exist. onbod is
	// package main so we can't import it; apply the schema directly.
	onbodPath := filepath.Join(storeDir, "onbod.db") + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)"
	onbodDB, err := sql.Open("sqlite", onbodPath)
	if err != nil {
		log.Fatalf("seed: open onbod.db: %v", err)
	}
	defer onbodDB.Close()
	if _, err := onbodDB.Exec(`
		CREATE TABLE IF NOT EXISTS invites (
			token         TEXT PRIMARY KEY,
			target_glob   TEXT NOT NULL,
			issued_by_sub TEXT NOT NULL,
			issued_at     TEXT NOT NULL,
			expires_at    TEXT,
			max_uses      INTEGER NOT NULL DEFAULT 1,
			used_count    INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS onboarding (
			jid TEXT PRIMARY KEY, status TEXT NOT NULL,
			prompted_at TEXT, created TEXT NOT NULL,
			token TEXT, token_expires TEXT, user_sub TEXT,
			gate TEXT, queued_at TEXT, admitted_at TEXT
		);
		CREATE TABLE IF NOT EXISTS onboarding_gates (
			gate TEXT PRIMARY KEY, limit_per_day INTEGER NOT NULL,
			enabled INTEGER NOT NULL DEFAULT 1
		);
		CREATE TABLE IF NOT EXISTS audit_log (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
			category        TEXT    NOT NULL,
			action          TEXT    NOT NULL,
			actor           TEXT    NOT NULL,
			actor_sub       TEXT,
			resource        TEXT,
			scope           TEXT,
			surface         TEXT,
			params_summary  TEXT,
			outcome         TEXT    NOT NULL DEFAULT '',
			error_msg       TEXT,
			duration_ms     INTEGER,
			turn_id         TEXT,
			folder          TEXT,
			instance        TEXT,
			request_id      TEXT,
			source_ip       TEXT
		);
		CREATE INDEX IF NOT EXISTS audit_log_created_at ON audit_log(created_at);
		CREATE INDEX IF NOT EXISTS audit_log_actor_sub  ON audit_log(actor_sub) WHERE actor_sub IS NOT NULL;
		CREATE INDEX IF NOT EXISTS audit_log_folder     ON audit_log(folder)    WHERE folder    IS NOT NULL;
		CREATE INDEX IF NOT EXISTS audit_log_cat_act    ON audit_log(category, action);
	`); err != nil {
		log.Fatalf("seed: onbod schema: %v", err)
	}

	now := time.Now().UTC()
	nowS := now.Format(time.RFC3339)

	if err := rdb.PutGroup(core.Group{Folder: "inbox", AddedAt: now, Product: "assistant"}); err != nil {
		log.Fatalf("seed: put group: %v", err)
	}

	sqlDB := rdb.SQL()

	if _, err := sqlDB.Exec(`INSERT INTO routes (seq, match, target) VALUES (?, ?, ?)`,
		0, "chat_jid=telegram:user/seed", "inbox"); err != nil {
		log.Fatalf("seed: insert route: %v", err)
	}

	if _, err := sqlDB.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at, granted_by)
		 VALUES ('testadmin', 'admin', '**', 'allow', ?, 'seed')`, nowS); err != nil {
		log.Fatalf("seed: insert acl: %v", err)
	}

	if err := rdb.PutMessage(core.Message{
		ID: "seed-msg-1", ChatJID: "telegram:user/seed",
		Sender: "testuser", Content: "hello from seed",
		Timestamp: now, Source: "telegram", Verb: "say",
	}); err != nil {
		log.Fatalf("seed: put message: %v", err)
	}

	if err := os.WriteFile(
		filepath.Join(dataDir, "groups", "inbox", "MEMORY.md"),
		[]byte("# seed MEMORY\n\nplaywright test marker M-MARK-XYZ\n"),
		0o644,
	); err != nil {
		log.Fatalf("seed: write MEMORY.md: %v", err)
	}

	fmt.Printf("seeded %s\n", dataDir)
}
