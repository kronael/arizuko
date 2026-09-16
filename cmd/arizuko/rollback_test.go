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
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
	"time"
)

// seedRoutdConfig puts a group and one egress rule into routd.db with direct
// SQL, then proves both landed — a fixture that silently failed would make
// every assertion below vacuous.
//
// The seed goes through store's own writers, which is what puts `updated_at` on
// the group: `groups.updated_at` is server-owned and never invented (its
// AbsentStamps declaration), so a group written any other way carries none and
// the checksum assertion below would be measuring that instead of the rollback.
func seedRoutdConfig(t *testing.T, db *store.Store) {
	t.Helper()
	st := db
	if err := st.PutGroupRow(core.Group{
		Folder:  "corp/eng",
		AddedAt: time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC),
		Product: "assistant",
	}); err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := st.AddNetworkRule("corp/eng", "original.example.com", "seed"); err != nil {
		t.Fatalf("seed rule: %v", err)
	}
	if got := groupCount(t, db, "corp/eng"); got != 1 {
		t.Fatalf("seeded group count = %d, want 1", got)
	}
	if got := ruleCount(t, db, "corp/eng", "original.example.com"); got != 1 {
		t.Fatalf("seeded rule count = %d, want 1", got)
	}
}

// groupCount answers how many group items name folder. An empty folder counts
// every group. It reads through store's own reader, so a fixture cannot agree
// with a bug the daemon has.
func groupCount(t *testing.T, db *store.Store, folder string) int {
	t.Helper()
	all := db.AllGroups()
	if folder == "" {
		return len(all)
	}
	if _, ok := all[folder]; ok {
		return 1
	}
	return 0
}

// ruleCount answers how many egress rules match folder and target. An empty
// folder or target is a wildcard for that half, which is what the `WHERE`
// clauses these replace left out.
func ruleCount(t *testing.T, db *store.Store, folder, target string) int {
	t.Helper()
	rules, err := db.AllNetworkRules()
	if err != nil {
		t.Fatalf("read network rules: %v", err)
	}
	n := 0
	for _, r := range rules {
		if folder != "" && r.Folder != folder {
			continue
		}
		if target != "" && r.Target != target {
			continue
		}
		n++
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
	routdSum, err := resreg.Checksum(resreg.On(stores[resreg.SubsystemRoutd].Obj()), resreg.SubsystemRoutd)
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
	routdDB := stores[resreg.SubsystemRoutd]
	seedRoutdConfig(t, routdDB)

	before, err := resreg.Checksum(resreg.On(routdDB.Obj()), resreg.SubsystemRoutd)
	if err != nil {
		t.Fatal(err)
	}
	beforeOriginal := ruleCount(t, routdDB, "corp/eng", "original.example.com")
	if beforeOriginal != 1 {
		t.Fatalf("pre-state wrong: original rule count = %d", beforeOriginal)
	}

	docs := forwardDocs(t, stores)
	_, aerr := applyDocs(context.Background(), stores, docs, applyRun{actor: "test"})
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
	if got := ruleCount(t, routdDB, "corp/eng", "original.example.com"); got != 1 {
		t.Errorf("corp/eng's original rule not restored (count=%d)", got)
	}
	if got := ruleCount(t, routdDB, "", "replaced.example.com"); got != 0 {
		t.Errorf("the forward apply's replacement rule survived the rollback (count=%d)", got)
	}
	if got := groupCount(t, routdDB, "new/team"); got != 0 {
		t.Errorf("the folder the forward apply CREATED survived the rollback (count=%d) — PruneScopes is not working", got)
	}
	if got := ruleCount(t, routdDB, "new/team", ""); got != 0 {
		t.Errorf("new/team's rules survived the rollback (count=%d)", got)
	}

	after, err := resreg.Checksum(resreg.On(routdDB.Obj()), resreg.SubsystemRoutd)
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
	routdDB := stores[resreg.SubsystemRoutd]
	seedRoutdConfig(t, routdDB)

	msgs := routdDB
	if err := msgs.PutMessage(core.Message{
		ID: "m1", ChatJID: "web:corp/eng", Sender: "u", Content: "hello",
		Timestamp: time.Date(2026, 8, 6, 0, 0, 1, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed message: %v", err)
	}
	if got := countMessages(t, routdDB, "web:corp/eng"); got != 1 {
		t.Fatalf("seeded message count = %d, want 1", got)
	}

	if _, err := applyDocs(context.Background(), stores, forwardDocs(t, stores), applyRun{actor: "test"}); err == nil {
		t.Fatal("applyDocs must fail")
	}
	m, ok := msgs.MessageByID("web:corp/eng", "m1")
	if !ok || m.Content != "hello" {
		t.Errorf("the rollback ate a message that was never part of the config projection (%+v)", m)
	}
}

// TestRollback_SuccessfulApplyKeepsBothSubsystems guards the other direction:
// a rollback that fired on success would silently undo every good apply, and
// the test above cannot see that.
func TestRollback_SuccessfulApplyKeepsBothSubsystems(t *testing.T) {
	_, stores := openInstance(t)
	routdDB := stores[resreg.SubsystemRoutd]
	onbodDB := stores[resreg.SubsystemOnbod]
	seedRoutdConfig(t, routdDB)

	docs := forwardDocs(t, stores)
	// Give the onbod document the checksum onbod.db actually has, so both
	// transactions commit.
	onbodSum, err := resreg.Checksum(resreg.On(onbodDB.Obj()), resreg.SubsystemOnbod)
	if err != nil {
		t.Fatal(err)
	}
	docs[1].checksum = onbodSum

	report, aerr := applyDocs(context.Background(), stores, docs, applyRun{actor: "test"})
	if aerr != nil {
		t.Fatalf("applyDocs: %v", aerr)
	}
	if len(report) != 2 {
		t.Fatalf("report has %d lines, want one per subsystem", len(report))
	}
	if got := groupCount(t, routdDB, "new/team"); got != 1 {
		t.Errorf("new/team not created (count=%d)", got)
	}
	if got := ruleCount(t, routdDB, "", "replaced.example.com"); got != 1 {
		t.Errorf("corp/eng's rule not replaced (count=%d)", got)
	}
	if got := ruleCount(t, routdDB, "", "original.example.com"); got != 0 {
		t.Errorf("the old rule survived a successful apply (count=%d)", got)
	}
	// The gate is an item, so it is read through the method onbod calls. This is
	// the one assertion in this file that reaches an entity-backed resource, and
	// it is what proves an apply on the OBJECT path landed rows a reader sees.
	gates, err := onbodDB.ListGates()
	if err != nil {
		t.Fatalf("ListGates: %v", err)
	}
	found := false
	for _, g := range gates {
		if g.Gate == "invite_required" {
			found = true
		}
	}
	if !found {
		t.Errorf("onbod gate not applied: %+v", gates)
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
