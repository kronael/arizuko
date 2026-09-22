package routd

import (
	"testing"

	"github.com/kronael/arizuko/store"
)

// The turn-side half of spec 5/33-paths-roles §"Two axes": that the attested sender actually
// REACHES the evaluator, and that turnAuthorize is the one place a turn's principals
// are bound. auth/authorize_axes_test.go covers the evaluator's own rules.

// axesDB seeds a folder with what a real one has at birth: the role:member floor
// (store.SeedMemberRoleTx's edge), and nothing above it.
func axesDB(t *testing.T, folder string) *DB {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.NewOver(db.Obj()).PutMembership("folder:"+folder, store.RoleMember, "test"); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestTurnAuthorize_SenderReachesTheEvaluator: a grant written against the SENDER
// authorizes the turn that sender triggered, and no other. Before this, the sender
// was computed (turnCallerSub) for the budget gate and dropped; every tool call
// authorized as the destination folder alone.
//
// Falsifiable: have turnAuthorize ignore t.trigger (pass "" as the sender) and the
// granted case denies.
func TestTurnAuthorize_SenderReachesTheEvaluator(t *testing.T) {
	const folder = "atlas/eng"
	db := axesDB(t, folder)
	addACL(t, db, "google:alice", "mcp:register_group", "atlas/**", "allow")
	srv := NewServer(db, nil, nil, nil, 0, "")

	byAlice := srv.turnAuthorize(turnMCP{folder: folder, trigger: "google:alice"})
	if !byAlice(folder, "mcp:register_group", nil) {
		t.Fatal("the sender's own grant must reach the turn they triggered")
	}
	byBob := srv.turnAuthorize(turnMCP{folder: folder, trigger: "google:bob"})
	if byBob(folder, "mcp:register_group", nil) {
		t.Fatal("a different sender must not inherit alice's grant")
	}
}

// TestTurnAuthorize_SenderCannotReachAnotherTenant is the hazard at the wiring
// level: alice's grant covers all of `atlas`, but a turn routed to `atlas/eng` may
// use it only inside `atlas/eng`. Otherwise speaking in one folder's chat lends that
// folder's agent your reach everywhere else.
//
// Falsifiable: drop Route from the Caller turnAuthorize builds — the sender becomes
// an unconfined principal and `atlas/ops` allows.
func TestTurnAuthorize_SenderCannotReachAnotherTenant(t *testing.T) {
	const folder = "atlas/eng"
	db := axesDB(t, folder)
	addACL(t, db, "google:alice", "mcp:register_group", "atlas/**", "allow")
	srv := NewServer(db, nil, nil, nil, 0, "")

	authorize := srv.turnAuthorize(turnMCP{folder: folder, trigger: "google:alice"})
	if authorize("atlas/ops", "mcp:register_group", nil) {
		t.Fatal("a sibling folder is outside this turn's subtree; the sender's grant must not reach it")
	}
	if !authorize("atlas/eng/child", "mcp:register_group", nil) {
		t.Fatal("a descendant of the turn's folder is inside; the grant must reach it")
	}
}

// TestTurnAuthorize_TimedTriggerIsNotAPerson: timed and system triggers map to
// service:routd (turnCallerSub), so a scheduled turn carries the folder's authority
// and nobody's personal grants. A cron must not run as whoever last spoke.
//
// Falsifiable: pass t.trigger straight through instead of turnCallerSub and the
// "system" trigger picks up the row seeded for it.
func TestTurnAuthorize_TimedTriggerIsNotAPerson(t *testing.T) {
	const folder = "atlas/eng"
	db := axesDB(t, folder)
	addACL(t, db, "timed", "mcp:register_group", "atlas/**", "allow")
	addACL(t, db, "system", "mcp:register_group", "atlas/**", "allow")
	srv := NewServer(db, nil, nil, nil, 0, "")

	for _, trigger := range []string{"", "timed", "timed-isolated:7", "system"} {
		if srv.turnAuthorize(turnMCP{folder: folder, trigger: trigger})(folder, "mcp:register_group", nil) {
			t.Fatalf("trigger %q is not a person; it must not carry a personal grant", trigger)
		}
	}
}

// TestTurnAuthorize_UnpairedSenderKeepsTheFloor: the krons case — 164 senders, one
// pairing token, zero acl rows keyed on a sender (BUGS L146). Their turns keep the
// role:member floor exactly.
//
// Falsifiable: drop Route from the Caller and the floor disappears with it.
func TestTurnAuthorize_UnpairedSenderKeepsTheFloor(t *testing.T) {
	const folder = "atlas/eng"
	db := axesDB(t, folder)
	srv := NewServer(db, nil, nil, nil, 0, "")

	for _, sender := range []string{"telegram:user/1112184352", "whatsapp:271317395882184@lid", ""} {
		authorize := srv.turnAuthorize(turnMCP{folder: folder, trigger: sender})
		if !authorize(folder, "mcp:send", nil) {
			t.Fatalf("sender %q lost the role:member messaging floor", sender)
		}
		if authorize(folder, "mcp:register_group", nil) {
			t.Fatalf("sender %q gained management the folder was never granted", sender)
		}
	}
}

// TestTurnAuthorize_ElevatedStillAllowsAll: /root is the explicit path by which a
// person's own authority raises a turn, and it is unconfined. It must survive the
// axis change untouched, because it is the mechanism the identity axis deliberately
// does NOT duplicate (auth.identityCovers).
//
// Falsifiable: drop the elevated arm from turnAuthorize and a /root turn is bounded
// to its own subtree.
func TestTurnAuthorize_ElevatedStillAllowsAll(t *testing.T) {
	const folder = "atlas/eng"
	db := axesDB(t, folder)
	srv := NewServer(db, nil, nil, nil, 0, "")
	authorize := srv.turnAuthorize(turnMCP{folder: folder, trigger: "google:root", elevated: true})
	if !authorize("some/other/tenant", "mcp:register_group", nil) {
		t.Fatal("/root elevation must stay unrestricted")
	}
}
