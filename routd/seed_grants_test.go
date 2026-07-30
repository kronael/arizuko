package routd

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/auth"
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
