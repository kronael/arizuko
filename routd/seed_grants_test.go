package routd

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/store"
)

// TestIntegration_GrantSourceDifferential (4/R cutover safety net, BUG-1 fixed):
// drives the ACTUAL production deriveFolderGrants (role-sourced) and asserts its
// decision equals the old grants.DeriveRules for every tier × tool. Both read the
// SAME routd DB as the platform source (no routes → platform empty on both sides),
// so this pins the tier→role equivalence through the real function — if it ever
// diverges, the flip changed what an agent may do. (Platform-verb flow-through is
// pinned by TestIntegration_PlatformVerbGrants + the routed case below.)
func TestIntegration_GrantSourceDifferential(t *testing.T) {
	tools := []struct {
		tool   string
		params map[string]string
	}{
		{"reply", nil}, {"send", nil}, {"send_file", nil}, {"register_group", nil},
		{"schedule_task", nil}, {"refresh_groups", nil}, {"list_tokens", nil},
		{"escalate_group", nil}, {"add_route", nil}, {"set_group_open", nil},
	}
	// Folders chosen for distinct real tiers (auth.Resolve = min(count("/"),3),
	// named top floored to 1): "" =0, "w" =1, "w/o/t" =2, "w/o/t/u" =3.
	for _, folder := range []string{"", "w", "w/o/t", "w/o/t/u"} {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		tier := auth.Resolve(folder).Tier // the SAME tier deriveFolderGrants uses
		newRules := deriveFolderGrants(db, folder)
		oldRules := grants.DeriveRules(db, folder, tier, worldOf(folder))
		for _, c := range tools {
			old := grants.CheckAction(oldRules, c.tool, c.params)
			neu := grants.CheckAction(newRules, c.tool, c.params)
			if old != neu {
				t.Errorf("folder %q (tier %d) %s: deriveFolderGrants=%v DeriveRules=%v (flip changed behavior)",
					folder, tier, c.tool, neu, old)
			}
		}
		db.Close()
	}
}

// TestTierRole_DecouplesGrantsFromDepth is the 4/R proof: a DEEP folder (tier-3
// depth, which by DeriveRules gets only reply/send_file/like/edit) bound to
// role:tier1 gains a tier-1 grant (register_group) via role expansion — capability
// no longer leaks from location. This is the mechanism the grant-surface flip rides.
func TestIntegration_RoleDecouplesDepth(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db.SQL())
	if err := SeedTierRoles(st); err != nil {
		t.Fatal(err)
	}

	const deep = "w/a/b/c" // tier 3: register_group NOT in the depth-derived bundle
	if slices.Contains(deriveFolderGrants(db, deep), "register_group") {
		t.Fatal("precondition: a tier-3 folder should not have register_group by depth")
	}
	// Bind the deep folder to role:tier1 — now it holds tier-1's bundle.
	if err := st.AddMembership("folder:"+deep, tierRoleName(1), "test"); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(deriveFolderGrants(db, deep), "register_group") {
		t.Fatal("role:tier1 binding must grant register_group to the deep folder (depth-decoupled)")
	}
}

// TestIntegration_OperatorDenyBeatsRoleAllow (adversary BUG 3): an operator DENY on
// folder:<path> for a verb the folder's role ALLOWS must win, regardless of the
// order store.ACLRowsFor returns rows (folder: sorts before role: in the index).
// The render partitions denies last so deny-precedence is order-independent.
func TestIntegration_OperatorDenyBeatsRoleAllow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db.SQL())
	if err := SeedTierRoles(st); err != nil {
		t.Fatal(err)
	}
	const f = "w" // bind to role:tier1 which allows register_group
	if err := st.AddMembership("folder:"+f, tierRoleName(1), "test"); err != nil {
		t.Fatal(err)
	}
	// Operator deny on the folder principal for a role-granted verb.
	addACL(t, db, "folder:"+f, "mcp:register_group", f, "deny")

	rules := folderGrantsFromACLOnly(st, f)
	if grants.CheckAction(rules, "register_group", nil) {
		t.Fatalf("operator deny must beat role allow (deny-precedence), rules=%v", rules)
	}
}

// TestSeedTierRoles_EnforcesAndSkips pins the H2 fix: a reseed enforces the
// canonical bundle on role:tier<N> (a stray row is removed) AND an unchanged
// reseed is a no-op (skip-if-unchanged) so a routine routd.Open / `arizuko
// packages` neither churns nor leaves the principal momentarily empty.
func TestSeedTierRoles_EnforcesAndSkips(t *testing.T) {
	db, err := OpenMem() // OpenMem seeds once
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db.SQL())

	role := tierRoleName(1)
	base := len(st.ListACL(role))
	if base == 0 {
		t.Fatal("precondition: role:tier1 should be seeded with a bundle")
	}
	// aclRowsEqual: the freshly-seeded set equals the desired set (skip path holds).
	if !aclRowsEqual(st.ListACL(role), tierRoleRows(role, 1)) {
		t.Fatal("seeded rows must equal desired bundle (skip-if-unchanged would never fire)")
	}
	// Inject a stray grant onto the system principal — the set now differs.
	if err := st.PutACLRow(core.ACLRow{
		Principal: role, Action: "mcp:__bogus__", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	if len(st.ListACL(role)) != base+1 {
		t.Fatalf("stray row not inserted: got %d want %d", len(st.ListACL(role)), base+1)
	}
	// Reseed: the atomic replace restores exactly the canonical bundle.
	if err := SeedTierRoles(st); err != nil {
		t.Fatal(err)
	}
	if got := len(st.ListACL(role)); got != base {
		t.Fatalf("reseed did not enforce canonical bundle: got %d want %d", got, base)
	}
	for _, r := range st.ListACL(role) {
		if r.Action == "mcp:__bogus__" {
			t.Fatal("stray row survived reseed — enforcement broken")
		}
	}
}

// TestBackfillFolderGrants_AdditiveAndIdempotent pins 4/R phase-(b) step 2: the
// backfill writes folder-scoped containment rows WITHOUT changing what
// deriveFolderGrants decides today (additive — the flip that reads the scopes is
// step 4), and is idempotent / skip-if-present.
func TestBackfillFolderGrants_AdditiveAndIdempotent(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db.SQL())

	folders := []string{"w", "w/o", "w/o/t"}
	for _, f := range folders {
		if err := db.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	checkTools := []string{"register_group", "schedule_task", "reply", "add_route",
		"invite_create", "escalate_group", "delegate_group", "network_allow"}
	decide := func(f string) map[string]bool {
		rules := deriveFolderGrants(db, f)
		m := map[string]bool{}
		for _, tool := range checkTools {
			m[tool] = grants.CheckAction(rules, tool, nil)
		}
		return m
	}

	before := map[string]map[string]bool{}
	for _, f := range folders {
		before[f] = decide(f)
	}

	if err := BackfillFolderGrants(st, folders); err != nil {
		t.Fatal(err)
	}

	// Additive: no deriveFolderGrants decision changed.
	for _, f := range folders {
		after := decide(f)
		for _, tool := range checkTools {
			if before[f][tool] != after[tool] {
				t.Errorf("backfill changed decision: folder %q tool %q %v→%v (must be additive)",
					f, tool, before[f][tool], after[tool])
			}
		}
	}

	// Scoped containment rows landed: tier-1 "w" gets register_group scoped w/*.
	if !alreadyBackfilled(st, "folder:w") {
		t.Fatal("backfill wrote no marker row for folder:w")
	}
	foundDirectChild := false
	for _, r := range st.ListACL("folder:w") {
		if r.Action == "mcp:register_group" && r.Scope == "w/*" {
			foundDirectChild = true
		}
	}
	if !foundDirectChild {
		t.Error("expected folder:w mcp:register_group scoped w/* (direct-child containment)")
	}

	// Idempotent + skip-if-present: a second run writes nothing.
	n := len(st.ListACL("folder:w"))
	if err := BackfillFolderGrants(st, folders); err != nil {
		t.Fatal(err)
	}
	if got := len(st.ListACL("folder:w")); got != n {
		t.Errorf("backfill not idempotent: folder:w rows %d→%d", n, got)
	}
}

// TestBackfillFolderGrants_LegacyScopedRowStillBackfills pins adversary finding 2:
// a folder carrying a legacy pre-4/R operator-scoped mcp row (not a backfill row)
// must STILL be backfilled — the skip-guard keys on the backfill marker, not on
// "any scoped row". Idempotency then keys on the marker too.
func TestBackfillFolderGrants_LegacyScopedRowStillBackfills(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := store.New(db.SQL())

	const f = "w"
	if err := db.PutGroup(core.Group{Folder: f}); err != nil {
		t.Fatal(err)
	}
	// Legacy operator row: scoped, mcp:, but NOT the backfill marker.
	if err := st.PutACLRow(core.ACLRow{
		Principal: "folder:" + f, Action: "mcp:send", Scope: f, Effect: "allow",
		GrantedBy: "operator",
	}); err != nil {
		t.Fatal(err)
	}

	if err := BackfillFolderGrants(st, []string{f}); err != nil {
		t.Fatal(err)
	}
	// The backfill ran despite the legacy row: marker rows are present.
	marked := 0
	for _, r := range st.ListACL("folder:" + f) {
		if r.GrantedBy == backfillGrantedBy {
			marked++
		}
	}
	if marked == 0 {
		t.Fatal("legacy scoped row wrongly blocked the backfill (finding 2 regression)")
	}
	// Second run is a no-op (idempotent on the marker).
	n := len(st.ListACL("folder:" + f))
	if err := BackfillFolderGrants(st, []string{f}); err != nil {
		t.Fatal(err)
	}
	if got := len(st.ListACL("folder:" + f)); got != n {
		t.Errorf("second backfill not idempotent: %d→%d", n, got)
	}
}

// TestAuthorizeContainment_MatchesStructural is the phase-(b) FLIP oracle: after
// the real BackfillFolderGrants seeds a folder, auth.AuthorizeContainment (which
// reads folder: scoped rows only, never role ** rows) must return the SAME
// containment decision as AuthorizeStructural — for every tool the folder's
// magnitude actually grants (bundle-held or an outbound verb). Tools the bundle
// denies are gated out separately post-flip (the firewall/db.Authorize), so their
// structural-vs-bundle magnitude discrepancy is not asserted here. Green = the
// wired flip (magnitude gate + AuthorizeContainment) reproduces today's decision.
func TestAuthorizeContainment_MatchesStructural(t *testing.T) {
	tools := []struct {
		name  string
		field int // 0=TargetFolder 1=TaskOwner 2=RouteTarget
	}{
		{"inspect_tasks", 1}, {"schedule_task", 1}, {"cancel_task", 1},
		{"reply", 0}, {"send", 0}, {"post", 0}, {"forward", 0},
		{"register_group", 0}, {"delegate_group", 0}, {"escalate_group", 0},
		{"add_route", 2}, {"set_routes", 2},
		{"set_group_open", 0}, {"observe_group", 0},
		{"invite_create", 0}, {"add_acl", 0}, {"list_acl", 0},
	}
	for _, folder := range []string{"w", "w/o", "w/o/t", "w/o/t/u"} {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
		st := store.New(db.SQL())
		if err := BackfillFolderGrants(st, []string{folder}); err != nil {
			t.Fatal(err)
		}
		tier := auth.Resolve(folder).Tier
		bundle := grants.DeriveRules(nil, folder, tier, auth.WorldOf(folder))
		id := auth.Resolve(folder)

		targets := []string{folder, folder + "/c", folder + "/c/d", auth.WorldOf(folder), "other", "other/y"}
		for i := 0; i < len(folder); i++ {
			if folder[i] == '/' {
				targets = append(targets, folder[:i])
			}
		}
		for _, tl := range tools {
			// Only assert where the folder's magnitude grants the tool (bundle-held
			// or an outbound verb) — else the combined post-flip gate denies anyway.
			if !auth.OutboundVerbs[tl.name] && !grants.CheckAction(bundle, tl.name, nil) {
				continue
			}
			for _, tgt := range targets {
				var at auth.AuthzTarget
				switch tl.field {
				case 1:
					at.TaskOwner = tgt
				case 2:
					at.RouteTarget = tgt
				default:
					at.TargetFolder = tgt
				}
				structural := auth.AuthorizeStructural(id, tl.name, at) == nil
				contain := auth.AuthorizeContainment(st, folder, tl.name, tgt, id.IsRoot) == nil
				if structural != contain {
					t.Errorf("folder=%q tool=%q target=%q: structural=%v containment=%v",
						folder, tl.name, tgt, structural, contain)
				}
			}
		}
		db.Close()
	}
}

// worldOf mirrors auth.WorldOf for the test without importing it twice.
func worldOf(folder string) string {
	if folder == "" {
		return ""
	}
	if i := indexByte(folder, '/'); i >= 0 {
		return folder[:i]
	}
	return folder
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
