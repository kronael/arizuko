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

	// onbod.db — needed for /dash/invites/. dashd opens it as dbOnbod.
	odb, err := store.OpenOnbod(storeDir)
	if err != nil {
		log.Fatalf("seed: open onbod.db: %v", err)
	}
	defer odb.Close()

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
