package main

// Cross-subsystem pre-image rollback (spec 5/8 §"Cross-subsystem apply:
// per-subsystem transaction, pre-image rollback"): routd and onbod are
// separate SQLite files with no shared transaction, so a failure on the
// second must put the first back.
//
// The failure is REAL, not simulated: the onbod document carries a checksum
// that does not match onbod.db, so resreg.Apply's own in-tx CAS recheck
// rejects it — the same rejection an operator gets when the DB moved under
// their export. Nothing here stubs or wraps Apply.

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// seedRoutdConfig puts a group and one egress rule into routd.db with direct
// SQL, then proves both landed — a fixture that silently failed would make
// every assertion below vacuous.
//
// updated_at is seeded deliberately. The engine auto-stamps any EMPTY
// StampedField on insert, so a groups row with a NULL updated_at gets a fresh
// timestamp on every re-insert — which makes even a plain no-op `export |
// apply` move the subsystem checksum once (BUGS F41, pre-existing and not
// about rollback). Seeding it puts the fixture in the state any instance that
// has been through the config lane once is already in, so the checksum
// assertion below measures the rollback and not F41.
func seedRoutdConfig(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO groups (folder, added_at, updated_at, product)
		 VALUES ('corp/eng', '2026-08-06T00:00:00Z', '2026-08-06T00:00:00Z', 'assistant')`); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO network_rules (folder, target, created_at, created_by)
		 VALUES ('corp/eng', 'original.example.com', '2026-08-06T00:00:00Z', 'seed')`); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM groups WHERE folder='corp/eng'`); got != 1 {
		t.Fatalf("seeded group count = %d, want 1", got)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM network_rules WHERE folder='corp/eng' AND target='original.example.com'`); got != 1 {
		t.Fatalf("seeded rule count = %d, want 1", got)
	}
}

func scalar(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// forwardDocs builds the two documents applyDocs will see: a routd document
// that both REPLACES corp/eng's rule and CREATES a brand-new folder, and an
// onbod document whose checksum is wrong on purpose.
//
// The new folder is the load-bearing half: the pre-image never mentions
// new/team, so the pre-image's own scopes would not prune it, and a rollback
// that only re-applied the pre-image would leave new/team's rows behind.
func forwardDocs(t *testing.T, stores map[string]*store.Store) []parsedDoc {
	t.Helper()
	routdSum, err := resreg.Checksum(stores[resreg.SubsystemRoutd].DB(), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	return []parsedDoc{
		{
			subsystem: resreg.SubsystemRoutd,
			checksum:  routdSum,
			manifest: map[string]any{
				"groups": []resources.GroupsRow{
					{Folder: "corp/eng", Product: "assistant"},
					{Folder: "new/team", Product: "assistant"},
				},
				"network_rules": []resources.NetworkRulesRow{
					{Folder: "corp/eng", Target: "replaced.example.com"},
					{Folder: "new/team", Target: "brandnew.example.com"},
				},
			},
		},
		{
			subsystem: resreg.SubsystemOnbod,
			checksum:  "sha256:deadbeef", // real CAS rejection, no stub
			manifest: map[string]any{
				"onboarding_gates": []resources.OnboardingGatesRow{
					{Gate: "invite_required", LimitPerDay: 0, Enabled: 0},
				},
			},
		},
	}
}

// TestRollback_LaterSubsystemFailureRestoresTheEarlierOne is the whole point
// of the section: routd commits, onbod is rejected, routd must end at its
// pre-apply content hash.
func TestRollback_LaterSubsystemFailureRestoresTheEarlierOne(t *testing.T) {
	_, stores := openInstance(t)
	routdDB := stores[resreg.SubsystemRoutd].DB()
	seedRoutdConfig(t, routdDB)

	before, err := resreg.Checksum(routdDB, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	beforeOriginal := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE folder='corp/eng' AND target='original.example.com'`)
	if beforeOriginal != 1 {
		t.Fatalf("pre-state wrong: original rule count = %d", beforeOriginal)
	}

	docs := forwardDocs(t, stores)
	_, aerr := applyDocs(context.Background(), stores, docs, false, "test", "")
	if aerr == nil {
		t.Fatal("applyDocs must fail: the onbod document's checksum does not match onbod.db")
	}
	if got := aerr.Error(); !containsAll(got, "onbod", "checksum mismatch") {
		t.Errorf("error %q must name the failing subsystem and the reason", got)
	}
	if containsAll(aerr.Error(), "ROLLBACK ALSO FAILED") {
		t.Fatalf("the rollback itself failed: %v", aerr)
	}

	// Content-level assertions, not just the hash: a hash-only check would
	// pass for a rollback that got lucky on a collision, and says nothing
	// about WHICH rows moved.
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE folder='corp/eng' AND target='original.example.com'`); got != 1 {
		t.Errorf("corp/eng's original rule not restored (count=%d)", got)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE target='replaced.example.com'`); got != 0 {
		t.Errorf("the forward apply's replacement rule survived the rollback (count=%d)", got)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM groups WHERE folder='new/team'`); got != 0 {
		t.Errorf("the folder the forward apply CREATED survived the rollback (count=%d) — PruneScopes is not working", got)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE folder='new/team'`); got != 0 {
		t.Errorf("new/team's rules survived the rollback (count=%d)", got)
	}

	after, err := resreg.Checksum(routdDB, resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("routd checksum after rollback = %s, want the pre-apply %s", after, before)
	}
}

// TestRollback_MessagesSurviveTheRollback pins the load-bearing constraint:
// the rollback restores the manifest PROJECTION, not the database. A message
// that arrived during the apply window is not config and must still be there
// — a whole-DB file swap would have eaten it.
func TestRollback_MessagesSurviveTheRollback(t *testing.T) {
	_, stores := openInstance(t)
	routdDB := stores[resreg.SubsystemRoutd].DB()
	seedRoutdConfig(t, routdDB)

	if _, err := routdDB.Exec(
		`INSERT INTO messages (id, chat_jid, sender, content, timestamp, is_from_me, is_bot_message)
		 VALUES ('m1', 'web:corp/eng', 'u', 'hello', '2026-08-06T00:00:01Z', 0, 0)`); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM messages`); got != 1 {
		t.Fatalf("seeded message count = %d, want 1", got)
	}

	if _, err := applyDocs(context.Background(), stores, forwardDocs(t, stores), false, "test", ""); err == nil {
		t.Fatal("applyDocs must fail")
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM messages WHERE id='m1' AND content='hello'`); got != 1 {
		t.Errorf("the rollback ate a message that was never part of the config projection (count=%d)", got)
	}
}

// TestRollback_SuccessfulApplyKeepsBothSubsystems guards the other direction:
// a rollback that fired on success would silently undo every good apply, and
// the test above cannot see that.
func TestRollback_SuccessfulApplyKeepsBothSubsystems(t *testing.T) {
	_, stores := openInstance(t)
	routdDB := stores[resreg.SubsystemRoutd].DB()
	onbodDB := stores[resreg.SubsystemOnbod].DB()
	seedRoutdConfig(t, routdDB)

	docs := forwardDocs(t, stores)
	// Give the onbod document the checksum onbod.db actually has, so both
	// transactions commit.
	onbodSum, err := resreg.Checksum(onbodDB, resreg.SubsystemOnbod)
	if err != nil {
		t.Fatal(err)
	}
	docs[1].checksum = onbodSum

	report, aerr := applyDocs(context.Background(), stores, docs, false, "test", "")
	if aerr != nil {
		t.Fatalf("applyDocs: %v", aerr)
	}
	if len(report) != 2 {
		t.Fatalf("report has %d lines, want one per subsystem", len(report))
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM groups WHERE folder='new/team'`); got != 1 {
		t.Errorf("new/team not created (count=%d)", got)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE target='replaced.example.com'`); got != 1 {
		t.Errorf("corp/eng's rule not replaced (count=%d)", got)
	}
	if got := scalar(t, routdDB, `SELECT COUNT(*) FROM network_rules WHERE target='original.example.com'`); got != 0 {
		t.Errorf("the old rule survived a successful apply (count=%d)", got)
	}
	if got := scalar(t, onbodDB, `SELECT COUNT(*) FROM onboarding_gates WHERE gate='invite_required'`); got != 1 {
		t.Errorf("onbod gate not applied (count=%d)", got)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
