package routd

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/store"
)

// fakeSrc feeds platformRules a fixed jid set.
type fakeSrc struct{ jids []string }

func (f fakeSrc) RouteSourceJIDsInWorld(string) []string { return f.jids }

// TestSeedFolderGrants_Differential (4/R cutover, the safety net): for every tier
// and a representative tool matrix, the decision from acl-sourced grants (after
// SeedFolderGrants) MUST equal the decision from DeriveRules. This is the old-vs-new
// equivalence the audit required before the flip: if these ever diverge, seeding
// dropped/mangled a rule and the tier deletion would change what an agent may do.
func TestSeedFolderGrants_Differential(t *testing.T) {
	tools := []struct {
		tool   string
		params map[string]string
	}{
		{"reply", nil},
		{"send", nil},
		{"send_file", nil},
		{"register_group", nil},
		{"schedule_task", nil},
		{"refresh_groups", nil},
		{"list_tokens", nil},
		{"like", map[string]string{"jid": "telegram:c/1"}},
		{"like", map[string]string{"jid": "discord:c/1"}},
		{"post", map[string]string{"jid": "telegram:c/1"}},
	}
	src := fakeSrc{jids: []string{"telegram:user/1"}}

	for tier := 0; tier <= 3; tier++ {
		folder := map[int]string{0: "", 1: "acme", 2: "acme/eng", 3: "acme/eng/sre"}[tier]
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		st := store.New(db.SQL())
		if err := SeedFolderGrants(st, folder, tier, src); err != nil {
			t.Fatal(err)
		}
		oldRules := grants.DeriveRules(src, folder, tier, worldOf(folder))
		newRules := folderGrantsFromACLOnly(st, folder)
		for _, c := range tools {
			old := grants.CheckAction(oldRules, c.tool, c.params)
			neu := grants.CheckAction(newRules, c.tool, c.params)
			if old != neu {
				t.Errorf("tier %d %s%v: acl-sourced=%v DeriveRules=%v (seeding not faithful)",
					tier, c.tool, c.params, neu, old)
			}
		}
		db.Close()
	}
}

// TestTierRole_DecouplesGrantsFromDepth is the 4/R proof: a DEEP folder (tier-3
// depth, which by DeriveRules gets only reply/send_file/like/edit) bound to
// role:tier1 gains a tier-1 grant (register_group) via role expansion — capability
// no longer leaks from location. This is the mechanism the grant-surface flip rides.
func TestTierRole_DecouplesGrantsFromDepth(t *testing.T) {
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
