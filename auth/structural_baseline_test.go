package auth

import "testing"

// AuthorizeStructural equivalence oracle for the 4/R cutover (spec 4/R safety
// audit, item 2). The tier→tool baseline in grants/ pins the mcp:* rule set but
// NOT the structural containment gate — the function step 5 proposes replacing
// with acl scope-globs. This freezes AuthorizeStructural's decisions across its
// gated tools at the tier + containment boundaries. When the cutover reproduces
// containment via seeded folder:<path> acl rows, these (tier, tool, target)
// decisions MUST still hold. A diff here = the tier deletion changed who may act
// on what — intended or a bug, never silent.
func TestAuthorizeStructuralBaseline_ForCutover(t *testing.T) {
	id := func(folder string, tier int) Identity { return Identity{Folder: folder, Tier: tier} }
	tf := func(f string) AuthzTarget { return AuthzTarget{TargetFolder: f} }

	cases := []struct {
		name  string
		id    Identity
		tool  string
		tgt   AuthzTarget
		allow bool
	}{
		// Read containment (task owner = self/descendant).
		{"inspect self task", id("acme/eng", 2), "inspect_tasks", AuthzTarget{TaskOwner: "acme/eng"}, true},
		{"inspect foreign task", id("acme/eng", 2), "inspect_tasks", AuthzTarget{TaskOwner: "other/x"}, false},

		// Session/topic containment (self OR descendant).
		{"reset own subtree", id("acme", 2), "reset_session", tf("acme/eng"), true},
		{"reset foreign", id("acme", 2), "reset_session", tf("other"), false},

		// inject_message: tier <=1 only.
		{"inject tier1", id("acme", 1), "inject_message", tf("acme"), true},
		{"inject tier2", id("acme/eng", 2), "inject_message", tf("acme/eng"), false},

		// register_group: tier <=1; tier1 must target a DIRECT child.
		{"register direct child", id("acme", 1), "register_group", tf("acme/eng"), true},
		{"register grandchild", id("acme", 1), "register_group", tf("acme/eng/x"), false},
		{"register tier2", id("acme/eng", 2), "register_group", tf("acme/eng/x"), false},

		// escalate_group: tier >=2 only.
		{"escalate tier1", id("acme", 1), "escalate_group", tf("acme"), false},
		{"escalate tier2", id("acme/eng", 2), "escalate_group", tf("acme/eng"), true},

		// delegate_group: not tier3; target must be a STRICT descendant.
		{"delegate to child", id("acme/eng", 2), "delegate_group", tf("acme/eng/x"), true},
		{"delegate to self", id("acme/eng", 2), "delegate_group", tf("acme/eng"), false},
		{"delegate tier3", id("acme/eng/sre", 3), "delegate_group", tf("acme/eng/sre/x"), false},

		// routes: tier <=1; tier1 confined to self/descendant route target.
		{"route own subtree", Identity{Folder: "acme", Tier: 1}, "add_route", AuthzTarget{RouteTarget: "acme/x"}, true},
		{"route tier2", Identity{Folder: "acme/eng", Tier: 2}, "add_route", AuthzTarget{RouteTarget: "acme/eng"}, false},

		// egress: tier <=1; tier1 subtree-confined.
		{"egress own subtree", id("acme", 1), "network_allow", tf("acme/x"), true},
		{"egress foreign", id("acme", 1), "network_allow", tf("other"), false},
		{"egress tier2", id("acme/eng", 2), "network_allow", tf("acme/eng"), false},

		// tasks: not tier3; tier2 gates on TaskOwner == own folder.
		{"schedule own", id("acme/eng", 2), "schedule_task", AuthzTarget{TaskOwner: "acme/eng"}, true},
		{"schedule foreign", id("acme/eng", 2), "schedule_task", AuthzTarget{TaskOwner: "other"}, false},
		{"schedule tier3", id("acme/eng/sre", 3), "schedule_task", AuthzTarget{TaskOwner: "acme/eng/sre"}, false},
	}
	for _, c := range cases {
		got := AuthorizeStructural(c.id, c.tool, c.tgt) == nil
		if got != c.allow {
			t.Errorf("%s: %s tier=%d → allow=%v want %v (baseline drift — cutover must reproduce)",
				c.name, c.tool, c.id.Tier, got, c.allow)
		}
	}
}
