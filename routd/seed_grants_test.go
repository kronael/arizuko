package routd

import (
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
