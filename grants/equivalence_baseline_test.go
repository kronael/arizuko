package grants

import "testing"

// fakeRouteSource feeds platformRules a fixed jid set so the baseline exercises
// the platform-scoped verb path (audit item 2b) the nil-source case skips.
type fakeRouteSource struct{ jids []string }

func (f fakeRouteSource) RouteSourceJIDsInWorld(string) []string { return f.jids }

// TestPlatformVerbBaseline_ForCutover freezes the platform-scoped verb decisions
// (like/dislike/post/… gated by jid=platform:*) that DeriveRules produces when a
// world has routed platforms. The cutover's role grants must reproduce these:
// a verb is allowed only for a platform the folder actually routes, and only with
// a matching jid param.
func TestIntegration_PlatformVerbGrants(t *testing.T) {
	src := fakeRouteSource{jids: []string{"telegram:user/1"}}
	rules := DeriveRules(src, "acme", 1, "acme") // tier 1 gets platformRules(world jids)

	cases := []struct {
		tool  string
		jid   string
		allow bool
	}{
		{"like", "telegram:chat/9", true},  // routed platform + matching jid
		{"like", "discord:chat/9", false},  // platform not routed
		{"like", "", false},                // platform verb needs a jid
		{"post", "telegram:chan/1", true},  // another platform verb
		{"reply", "telegram:chat/9", true}, // reply is both basic + platform
	}
	for _, c := range cases {
		params := map[string]string{}
		if c.jid != "" {
			params["jid"] = c.jid
		}
		if got := CheckAction(rules, c.tool, params); got != c.allow {
			t.Errorf("tier1 %s jid=%q → allow=%v want %v (platform baseline drift)",
				c.tool, c.jid, got, c.allow)
		}
	}
}

// Equivalence baseline for the 4/R tier→role cutover (spec 4/R "strangler-fig
// cutover order", step 2). This freezes the CURRENT tier→tool authz decision
// produced by DeriveRules + CheckAction. When phase 2 seeds default roles and
// flips Authorize to read acl rows, the new acl-sourced decision MUST reproduce
// this table EXACTLY for the same (tier, tool) — that equivalence is the safety
// net that lets the tier system be deleted without changing what any agent may
// call. If this table changes, the cutover changed behavior: intended or a bug,
// never silent.
//
// Source (nil): no RouteSource, so platform-scoped verbs (like/edit via a jid
// param) are out of scope here — this pins the depth/tier gating that the
// default-role bundles must carry, not the per-platform send fan-out.
func TestIntegration_TierToolGrants(t *testing.T) {
	// want[tool] = allowed at [tier0, tier1, tier2, tier3].
	want := map[string][4]bool{
		"reply":          {true, true, true, true},
		"send_file":      {true, true, true, true},
		"send":           {true, true, true, false}, // tier-3 set omits send
		"register_group": {true, true, false, false}, // tier-1 fixed action only
		"schedule_task":  {true, true, false, false},
		"refresh_groups": {true, true, true, false}, // tier-1 + explicit tier-2
		"list_tokens":    {true, true, true, false}, // self-service read (t1/t2)
	}
	for tool, exp := range want {
		for tier := 0; tier <= 3; tier++ {
			rules := DeriveRules(nil, "acme/eng", tier, "acme")
			got := CheckAction(rules, tool, nil)
			if got != exp[tier] {
				t.Errorf("tier %d %s: got %v want %v (baseline drift — the cutover must reproduce this)",
					tier, tool, got, exp[tier])
			}
		}
	}
}
