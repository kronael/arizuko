// seed: prepare a throwaway DATA_DIR for the playwright dashd suite.
//
// Creates the dir layout, runs migrations on routd.db (routd.Open) and seeds:
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
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/store"
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

	// routd's own tables — acl, groups, routes, secrets, messages.
	rdb, err := routd.Open()
	if err != nil {
		log.Fatalf("seed: open routd's store: %v", err)
	}
	defer rdb.Close()

	// onbod's own tables — /dash/invites/ reads them. onbod is package main so
	// the seed cannot import its opener, and it asks the owners that DECLARE
	// onbod's schema instead of holding a copy of it. A hand-copied schema is a
	// second declaration of a table onbod already declares, and one had already
	// drifted: invites moved to a `ref` primary key while the copy here still
	// said `token`, so every invites test failed on "no such column: ref" and
	// read as a product bug.
	onbodObj, err := store.Handle(store.OwnerOnbod)
	if err != nil {
		log.Fatalf("seed: dial onbod's store: %v", err)
	}
	if err := store.PrepareOwner(context.Background(), onbodObj, store.OwnerOnbod); err != nil {
		log.Fatalf("seed: onbod entities: %v", err)
	}
	if err := resreg.EnsureSubsystem(context.Background(), onbodObj, onbodObj, resreg.SubsystemOnbod); err != nil {
		log.Fatalf("seed: onbod subsystem: %v", err)
	}

	// runed's own tables — /dash/runed/ needs them.
	runedDB, err := runed.Open()
	if err != nil {
		log.Fatalf("seed: open runed's store: %v", err)
	}
	defer runedDB.Close()

	now := time.Now().UTC()
	nowS := now.Format(time.RFC3339)

	if err := rdb.PutGroup(core.Group{Folder: "inbox", AddedAt: now, Product: "assistant"}); err != nil {
		log.Fatalf("seed: put group: %v", err)
	}

	// routd's registered resources are objstore entities, and store owns every
	// coordinate and attribute name they carry. The fixture writes through it so
	// the items dashd reads are the items the daemons write — a second rendering
	// here would put a row where the page's read does not look, and the suite
	// would report an empty page as a product bug.
	rst := store.NewOver(rdb.Obj())

	if _, err := rst.PutRouteRow(core.Route{
		Seq: 0, Match: "chat_jid=telegram:user/seed", Target: "inbox",
	}); err != nil {
		log.Fatalf("seed: insert route: %v", err)
	}

	if err := rst.PutACLRow(core.ACLRow{
		Principal: "testadmin", Action: "admin", Scope: "**",
		Effect: "allow", GrantedAt: core.TS(now), GrantedBy: "seed",
	}); err != nil {
		log.Fatalf("seed: insert acl: %v", err)
	}

	if err := rdb.PutMessage(core.Message{
		ID: "seed-msg-1", ChatJID: "telegram:user/seed",
		Sender: "testuser", Content: "hello from seed",
		Timestamp: now, Source: "telegram", Verb: "say",
	}); err != nil {
		log.Fatalf("seed: put message: %v", err)
	}
	// Flag the chat errored so the portal alert banner appears in tests. The
	// flag is the CHAT's — the banner reads ErroredChatJIDs — so the fixture
	// writes it where the page looks.
	if err := rst.MarkChatErrored("telegram:user/seed"); err != nil {
		log.Fatalf("seed: mark chat errored: %v", err)
	}

	// One chat route_token so /dash/tokens/ has a row to list and revoke.
	// dashd stopped minting tokens (the mint form was deleted 2026-08-15 —
	// tokens are minted where they are owned), so the page cannot create its
	// own fixture any more and the suite's create-then-revoke test had quietly
	// degraded into an early return.
	// The digest IS the item's coordinate, so the fixture names one directly
	// through the import path rather than minting a token nothing reads back:
	// the page lists and revokes by owner folder, and InsertRouteToken would add
	// an audit row this fixture never had.
	if _, err := rst.ImportRouteTokens(context.Background(), []store.ArchiveRouteTokenRow{{
		TokenHash:   "seed-token-hash-32-bytes-padding",
		JID:         "web:inbox",
		OwnerFolder: "inbox",
		CreatedAt:   core.TS(now),
	}}); err != nil {
		log.Fatalf("seed: insert route_token: %v", err)
	}

	// pending_actions for /dash/approvals/ and the portal's held-approvals
	// banner. The banner counts straight out of routd.db; the page itself reads
	// routd over HTTP, which the stub upstream answers.
	// `pending_actions` is declared by store and WRITTEN by routd, so the fixture
	// renders the item through store's exported coordinate and attribute names —
	// the same two the daemon writes it with. An empty reviewed_by is left OFF:
	// absent is the only null this model has.
	var held []objstore.Write
	for _, pa := range []struct{ id, tool, args, status, reviewedBy string }{
		{"seedheld", "like", `{"chatJid":"telegram:user/seed","targetId":"1","reaction":"+1"}`, "held", ""},
		{"seedgone", "delete", `{"chatJid":"telegram:user/seed","targetId":"2"}`, "rejected", "testadmin"},
	} {
		key, err := store.PendingActionKey(pa.id)
		if err != nil {
			log.Fatalf("seed: pending_action %s: %v", pa.id, err)
		}
		attrs := map[string]string{
			store.AttrPendGroupFolder: "inbox",
			store.AttrPendCallerAgent: "folder:inbox",
			store.AttrPendTool:        pa.tool,
			store.AttrPendArgs:        pa.args,
			store.AttrPendStatus:      pa.status,
			store.AttrPendChatJID:     "telegram:user/seed",
			store.AttrPendCreatedAt:   nowS,
		}
		if pa.reviewedBy != "" {
			attrs[store.AttrPendReviewedBy] = pa.reviewedBy
		}
		held = append(held, objstore.Write{
			Op: objstore.Put, Entity: store.PendingActions, Key: key, Attrs: attrs,
		})
	}
	if err := rst.WriteEntities(context.Background(), nil, held...); err != nil {
		log.Fatalf("seed: insert pending_actions: %v", err)
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
