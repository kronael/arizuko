// seed: prepare a throwaway DATA_DIR for the playwright dashd suite.
//
// Creates the dir layout (store/, groups/), runs schema migrations on a
// fresh sqlite, and seeds:
//   - one group `solo/inbox`
//   - one route targeting it
//   - ACL grants: principal `testadmin` -> action `admin` scope `**` (operator),
//     principal `testuser` -> no admin grants (write attempts must 403)
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

	for _, sub := range []string{"store", "groups", "groups/inbox"} {
		if err := os.MkdirAll(filepath.Join(dataDir, sub), 0o755); err != nil {
			log.Fatalf("seed: mkdir %s: %v", sub, err)
		}
	}

	dbPath := filepath.Join(dataDir, "store", "messages.db")
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		log.Fatalf("seed: open db: %v", err)
	}
	defer db.Close()

	if err := store.Migrate(db); err != nil {
		log.Fatalf("seed: migrate: %v", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if _, err := db.Exec(
		`INSERT INTO groups (folder, added_at, product) VALUES (?, ?, 'assistant')`,
		"inbox", now,
	); err != nil {
		log.Fatalf("seed: insert group: %v", err)
	}

	if _, err := db.Exec(
		`INSERT INTO routes (seq, match, target) VALUES (?, ?, ?)`,
		0, "chat_jid=telegram:user/seed", "inbox",
	); err != nil {
		log.Fatalf("seed: insert route: %v", err)
	}

	// Operator-tier admin via the `**` scope row.
	if _, err := db.Exec(
		`INSERT INTO acl (principal, action, scope, effect, granted_at, granted_by)
		 VALUES ('testadmin', 'admin', '**', 'allow', ?, 'seed')`,
		now,
	); err != nil {
		log.Fatalf("seed: insert acl operator: %v", err)
	}

	// Seed a sample message for /dash/activity to render.
	if _, err := db.Exec(
		`INSERT INTO messages
		   (id, chat_jid, sender, content, timestamp, source, verb)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"seed-msg-1", "telegram:user/seed", "testuser", "hello from seed",
		now, "telegram", "say",
	); err != nil {
		log.Fatalf("seed: insert message: %v", err)
	}

	// MEMORY.md for the seeded group so /dash/memory has content.
	if err := os.WriteFile(
		filepath.Join(dataDir, "groups", "inbox", "MEMORY.md"),
		[]byte("# seed MEMORY\n\nplaywright test marker M-MARK-XYZ\n"),
		0o644,
	); err != nil {
		log.Fatalf("seed: write MEMORY.md: %v", err)
	}

	fmt.Printf("seeded %s\n", dataDir)
}
