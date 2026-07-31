package auth

import "testing"

// TestBackfillScopes_EquivalentToStructural is the phase-(b) equivalence oracle
// (the safety net step (d) lacked — CTO 2026-07-31): for every structural tool ×
// real tier (1/2/3) × representative target, the scope-glob decision
// (BackfillScopes + ScopesMatch) MUST equal AuthorizeStructural's decision. Keyed
// on folder PATHS, never the tier scalar. Green here is the gate to writing the
// backfill migration and deleting AuthorizeStructural.
//
// Tier 0 (operator/root, "" folder) is IsRoot-handled at runtime, not backfilled,
// so it is out of scope here. Empty/absent targets (route to self, own-folder
// default) are resolved to the caller folder by the handlers before the scope
// layer, so only non-empty targets are enumerated.
func TestBackfillScopes_EquivalentToStructural(t *testing.T) {
	// target field each tool reads from AuthzTarget.
	const (
		fTarget = iota // TargetFolder
		fTask          // TaskOwner
		fRoute         // RouteTarget
	)
	tools := []struct {
		name  string
		field int
	}{
		{"list_tasks", fTask}, {"inspect_tasks", fTask},
		{"schedule_task", fTask}, {"pause_task", fTask},
		{"resume_task", fTask}, {"cancel_task", fTask},
		{"reset_session", fTarget}, {"fork_topic", fTarget},
		{"send", fTarget}, {"reply", fTarget}, {"post", fTarget}, {"forward", fTarget},
		{"inject_message", fTarget},
		{"register_group", fTarget}, {"escalate_group", fTarget}, {"delegate_group", fTarget},
		{"list_routes", fRoute}, {"set_routes", fRoute}, {"add_route", fRoute}, {"delete_route", fRoute},
		{"network_allow", fTarget}, {"network_deny", fTarget}, {"network_list", fTarget},
		{"set_group_open", fTarget}, {"set_observe_window", fTarget},
		{"observe_group", fTarget}, {"unobserve_group", fTarget},
		{"get_grants", fTarget}, {"set_grants", fTarget}, {"list_acl", fTarget},
		{"invite_create", fTarget}, {"invite_revoke", fTarget},
		{"add_acl", fTarget}, {"remove_acl", fTarget},
	}
	// (tier, caller folder) — the real backfill domain.
	callers := []struct {
		tier   int
		folder string
	}{
		{1, "w"},
		{2, "w/o"},
		{3, "w/o/t"},
	}
	// Non-empty targets: self, direct child, deep descendant, sibling, ancestor,
	// same-world other-branch, cross-world.
	targetsFor := func(f string) []string {
		switch f {
		case "w":
			return []string{"w", "w/o", "w/o/t", "w/x", "other", "other/y"}
		case "w/o":
			return []string{"w", "w/o", "w/o/t", "w/o/t/u", "w/x", "other"}
		default: // w/o/t
			return []string{"w", "w/o", "w/o/t", "w/o/t/u", "w/x", "other"}
		}
	}

	for _, tl := range tools {
		for _, c := range callers {
			id := Identity{Folder: c.folder, Tier: c.tier, IsRoot: c.tier == 0}
			scopes := BackfillScopes(tl.name, c.tier, c.folder)
			for _, tgt := range targetsFor(c.folder) {
				var at AuthzTarget
				switch tl.field {
				case fTask:
					at.TaskOwner = tgt
				case fRoute:
					at.RouteTarget = tgt
				default:
					at.TargetFolder = tgt
				}
				want := AuthorizeStructural(id, tl.name, at) == nil
				got := scopes != nil && ScopesMatch(scopes, tgt)
				if want != got {
					t.Errorf("%s tier=%d folder=%q target=%q: structural=%v scope=%v (scopes=%v)",
						tl.name, c.tier, c.folder, tgt, want, got, scopes)
				}
			}
		}
	}
}
