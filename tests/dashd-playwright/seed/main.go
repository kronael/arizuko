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
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/db_utils"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

func main() {
	var dataDir, onbodMigrations string
	flag.StringVar(&dataDir, "data", "", "DATA_DIR to seed (required)")
	flag.StringVar(&onbodMigrations, "onbod-migrations", "onbod/migrations",
		"directory of onbod's .sql migrations, relative to the repo root")
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
	rdb, err := routd.Create(storeDir)
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

	// onbod.db — /dash/invites/ reads it. onbod is package main so the seed
	// cannot import its opener; it applies onbod's OWN migration files instead
	// of copying their DDL. A hand-copied schema is a second declaration of a
	// table onbod already declares, and it had already drifted: invites moved to
	// a `ref` primary key in 0003 while the copy here still said `token`, so
	// every invites test failed on "no such column: ref" and read as a product
	// bug.
	//
	// The path comes from store.OwnerDBPath, never filepath.Join: since the
	// owner-subdir split dashd resolves onbod.db as store/onbod/onbod.db, and a
	// seed writing store/onbod.db left the page reporting "invites store
	// unavailable" while the suite still called itself green.
	onbodFile := store.OwnerDBPath(storeDir, store.OwnerOnbod)
	if err := os.MkdirAll(filepath.Dir(onbodFile), 0o755); err != nil {
		log.Fatalf("seed: mkdir onbod dir: %v", err)
	}
	onbodDB, err := db_utils.OpenWAL(db_utils.FileDSN(onbodFile))
	if err != nil {
		log.Fatalf("seed: open onbod.db: %v", err)
	}
	defer onbodDB.Close()
	applySQLDir(onbodDB, onbodMigrations)

	// runed.db — /dash/runed/ needs it, and runed IS an importable package, so
	// its own opener runs its own migrations.
	runedDB, err := runed.Create(storeDir)
	if err != nil {
		log.Fatalf("seed: open runed.db: %v", err)
	}
	defer runedDB.Close()

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
	// Mark one message errored so the portal alert banner appears in tests.
	if _, err := sqlDB.Exec(`UPDATE messages SET errored=1 WHERE id='seed-msg-1'`); err != nil {
		log.Fatalf("seed: mark errored: %v", err)
	}

	// One chat route_token so /dash/tokens/ has a row to list and revoke.
	// dashd stopped minting tokens (the mint form was deleted 2026-08-15 —
	// tokens are minted where they are owned), so the page cannot create its
	// own fixture any more and the suite's create-then-revoke test had quietly
	// degraded into an early return.
	if _, err := sqlDB.Exec(
		`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, kind)
		 VALUES (?, 'web:inbox', 'inbox', ?, ?)`,
		[]byte("seed-token-hash-32-bytes-padding"), nowS, store.RouteTokenKindRoute,
	); err != nil {
		log.Fatalf("seed: insert route_token: %v", err)
	}

	// pending_actions for /dash/approvals/ and the portal's held-approvals
	// banner. The banner counts straight out of routd.db; the page itself reads
	// routd over HTTP, which the stub upstream answers.
	for _, pa := range []struct{ id, tool, args, status, reviewedBy string }{
		{"seedheld", "like", `{"chatJid":"telegram:user/seed","targetId":"1","reaction":"+1"}`, "held", ""},
		{"seedgone", "delete", `{"chatJid":"telegram:user/seed","targetId":"2"}`, "rejected", "testadmin"},
	} {
		if _, err := sqlDB.Exec(
			`INSERT INTO pending_actions
			   (id, group_folder, caller_agent, tool, args, args_final, args_hash,
			    status, chat_jid, created_at, reviewed_by)
			 VALUES (?, 'inbox', 'folder:inbox', ?, ?, ?, ?, ?, 'telegram:user/seed', ?, ?)`,
			pa.id, pa.tool, pa.args, pa.args, pa.id+"-hash", pa.status, nowS, pa.reviewedBy,
		); err != nil {
			log.Fatalf("seed: insert pending_action %s: %v", pa.id, err)
		}
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

// applySQLDir runs every .sql file in dir, in name order — the same order
// db_utils.Migrate walks an embed.FS. Reading from disk rather than embedding
// is deliberate: the seed lives outside the daemons' package trees, and Go's
// embed cannot reach across them, so the choice is "read the real files" or
// "keep a copy that drifts".
func applySQLDir(db *sql.DB, dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		log.Fatalf("seed: read %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		log.Fatalf("seed: no .sql files in %s", dir)
	}
	sort.Strings(names)
	for _, n := range names {
		body, rerr := os.ReadFile(filepath.Join(dir, n))
		if rerr != nil {
			log.Fatalf("seed: read %s: %v", n, rerr)
		}
		if _, eerr := db.Exec(string(body)); eerr != nil {
			log.Fatalf("seed: apply %s: %v", n, eerr)
		}
	}
}
