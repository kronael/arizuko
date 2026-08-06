package routd

import (
	"os"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// TestMigration0030BackfillsTheMemberFloor pins the 5/33 floor as DATA.
//
// Migration 0023 seeded role:member's grant rows, then reassigned membership with
// `WHERE parent LIKE 'role:tier%'` — but the tier was DERIVED (min(depth,3)), never a
// stored membership, so the copy matched nothing and every pre-existing folder came out
// a member of nothing. On all three live instances that left 0 role:member edges against
// 15/7/7 groups, so routd's per-turn Visible view advertised ZERO tools to every agent.
// It read as "the agent works" only because prose rides submit_turn, not the reply tool.
//
// A folder that exists must hold the floor, however it got there.
func TestMigration0030BackfillsTheMemberFloor(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open routd.db: %v", err)
	}
	defer db.Close()

	// Insert a group the way a pre-0030 instance holds one: a raw row, with no
	// PutGroup → assignDefaultRole edge. This is the state 0023 left behind.
	if _, err := db.SQL().Exec(
		`INSERT INTO groups (folder, added_at) VALUES ('legacy', '2026-01-01T00:00:00Z')`,
	); err != nil {
		t.Fatalf("seed legacy group: %v", err)
	}
	if _, err := db.SQL().Exec(
		`DELETE FROM acl_membership WHERE child = 'folder:legacy'`,
	); err != nil {
		t.Fatalf("clear edge: %v", err)
	}

	st := store.New(db.SQL())
	held := auth.EffectiveActions(st, auth.Caller{Principal: "folder:legacy"})
	if held("mcp:reply") {
		t.Fatal("precondition: folder:legacy should hold nothing before the backfill")
	}

	// Run the SHIPPED migration file, not a copy of its SQL — an inlined copy would
	// keep passing if the file were emptied or deleted, which is exactly the vacuous
	// test this codebase keeps finding.
	sqlBytes, err := os.ReadFile("migrations/0030-backfill-role-member.sql")
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	if _, err := db.SQL().Exec(string(sqlBytes)); err != nil {
		t.Fatalf("apply migration: %v", err)
	}

	held = auth.EffectiveActions(store.New(db.SQL()), auth.Caller{Principal: "folder:legacy"})
	for _, tool := range []string{"reply", "send", "send_file", "send_voice", "edit"} {
		if !held("mcp:" + tool) {
			t.Errorf("after backfill folder:legacy lacks mcp:%s — the messaging floor is not seeded", tool)
		}
	}
}

// TestNewGroupIsBornWithTheFloor covers the other half: PutGroup must write the edge, so
// a group created after 0030 never depends on a backfill running again.
func TestNewGroupIsBornWithTheFloor(t *testing.T) {
	dir := t.TempDir()
	db, err := Open(dir)
	if err != nil {
		t.Fatalf("open routd.db: %v", err)
	}
	defer db.Close()

	if err := db.PutGroup(core.Group{Folder: "fresh"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	held := auth.EffectiveActions(store.New(db.SQL()), auth.Caller{Principal: "folder:fresh"})
	if !held("mcp:send_file") {
		t.Fatal("a newly created group does not hold the messaging floor")
	}
}
