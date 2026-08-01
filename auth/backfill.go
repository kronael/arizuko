package auth

import (
	"fmt"

	"github.com/kronael/arizuko/store"
)

// BackfillGrantedBy marks acl rows the 4/R backfill/create-time-delegation writes as
// CONTAINMENT (consumed by AuthorizeContainment). AuthorizeWith excludes them from
// MAGNITUDE, and the string firewall excludes them too — they never widen "has the
// tool", only bound "may act on this target".
const BackfillGrantedBy = "system:backfill-4r"

// AuthorizeContainment is phase (b)'s data-driven replacement for
// AuthorizeStructural's CONTAINMENT half: it allows `tool` against `target` iff the
// caller's own folder principal holds a backfilled/delegated `mcp:<tool>` allow row
// whose scope covers `target`. It reads `folder:<callerFolder>` rows ONLY — never
// role rows — so the `**`-scoped tier bundles (which would defeat containment) are
// irrelevant here. Magnitude (may the folder use the tool at all) is a SEPARATE
// gate (the string firewall / db.Authorize on the tool), already applied before
// this. Root bypasses. Empty target denies (mirrors authorizeOutbound's no-route).
func AuthorizeContainment(st *store.Store, callerFolder, tool, target string, isRoot bool) error {
	if isRoot {
		return nil
	}
	if target == "" {
		return fmt.Errorf("forbidden: %s has no resolved target", tool)
	}
	// 1. Data path: the folder's own scoped grants (backfilled at boot/create, or
	//    delegated). Precedence lets a broader delegated grant widen containment.
	for _, r := range st.ListACL("folder:" + callerFolder) {
		if r.Action == "mcp:"+tool && r.Effect == "allow" && matchPattern(r.Scope, target) {
			return nil
		}
	}
	// 2. Transitional fallback (phase b): the tier-computed containment scope. Keeps
	//    the flip equivalence-preserving even for a folder not yet backfilled — the
	//    exact analogue of AuthorizeWith's DeriveRules fallback the grant-flip kept.
	//    Removed in phase (e) when the tier scalar goes and data becomes the sole source.
	if ScopesMatch(BackfillScopes(tool, Resolve(callerFolder).Tier, callerFolder), target) {
		return nil
	}
	return fmt.Errorf("forbidden: %s on %s is outside %s's granted scope",
		tool, target, callerFolder)
}

// backfill.go is phase (b) of 4/R (specs/4/R §"Phase (b) cutover design"): it
// renders, for a folder at a given tier, the SCOPE GLOBS that reproduce
// AuthorizeStructural's per-tool containment as acl-row scopes. The backfill
// migration writes these onto folder:<path> principals so containment becomes
// DATA (a scope matched by AuthorizeWith) instead of the tree-shape code in
// policy.go. INERT until the migration + call-site flip land — its only current
// consumer is the equivalence oracle (backfill_test.go) that proves it matches
// AuthorizeStructural for every tool × tier × target before any fleet change.
//
// Returns the scope globs a folder of `tier` gets for `tool`; nil = the tier may
// NOT use the tool (the magnitude gate — in the new model the grant is simply
// absent from the bundle). Tier 0 is the operator/root identity ("" folder), which
// is IsRoot-handled at runtime and NEVER backfilled (auth.Resolve floors any
// non-empty folder to tier ≥ 1). The tier-0 arms return "**" for convenience but
// are NOT authoritative — e.g. AuthorizeStructural's "worlds are CLI-only" check on
// tier-0 register_group is an IsRoot concern this function cannot express (it takes
// no IsRoot). The equivalence oracle covers real folders (tier ≥ 1) only.
func BackfillScopes(tool string, tier int, folder string) []string {
	selfOrDesc := folder + "/**"      // F and every descendant
	directChild := folder + "/*"      // exactly one level below F
	strictDesc := folder + "/*/**"    // at least one level below F (not F)
	ownWorld := WorldOf(folder) + "/**"
	const any = "**"

	switch tool {
	// Read/own-subtree tools — every tier, containment self-or-descendant.
	case "list_tasks":
		return []string{any}
	case "inspect_tasks":
		return []string{selfOrDesc}
	case "reset_session", "fork_topic":
		return []string{selfOrDesc}
	case "send", "send_file", "send_voice", "reply", "post", "like", "dislike",
		"delete", "edit", "forward", "quote", "repost",
		"pin_message", "unpin_message", "unpin_all",
		"pane_set_prompts", "pane_set_title":
		// authorizeOutbound: self-or-descendant on the target chat's folder (root
		// bypasses via IsRoot at runtime — not a backfilled scope).
		return []string{selfOrDesc}

	case "inject_message":
		if tier > 1 {
			return nil
		}
		return []string{any}

	case "register_group":
		if tier >= 2 {
			return nil
		}
		if tier == 1 {
			return []string{directChild}
		}
		return []string{any} // tier 0 (operator) — worlds are CLI-only, IsRoot-gated

	case "escalate_group":
		if tier < 2 {
			return nil
		}
		return []string{any}

	case "delegate_group":
		if tier == 3 {
			return nil
		}
		return []string{strictDesc}

	case "list_routes", "set_routes", "add_route", "delete_route":
		if tier >= 2 {
			return nil
		}
		if tier == 1 {
			return []string{selfOrDesc}
		}
		return []string{any}

	case "network_allow", "network_deny", "network_list":
		if tier >= 2 {
			return nil
		}
		if tier == 1 {
			return []string{selfOrDesc}
		}
		return []string{any}

	case "schedule_task", "pause_task", "resume_task", "cancel_task":
		switch tier {
		case 3:
			return nil
		case 2:
			return []string{folder} // own folder only
		case 1:
			return []string{ownWorld}
		default:
			return []string{any}
		}

	case "set_group_open", "set_observe_window":
		if tier > 1 {
			return nil
		}
		if tier == 1 {
			return []string{selfOrDesc}
		}
		return []string{any}

	case "observe_group", "unobserve_group":
		if tier >= 3 {
			return nil
		}
		if tier == 2 {
			// subtree OR parent-chain: F/** plus each strict ancestor (exact scope).
			return append([]string{selfOrDesc}, ancestorScopes(folder)...)
		}
		return []string{any}

	case "get_grants", "set_grants", "list_acl",
		"invite_create", "invite_revoke", "add_acl", "remove_acl":
		if tier >= 2 {
			return nil
		}
		if tier == 1 {
			return []string{ownWorld}
		}
		return []string{any}
	}
	return nil
}

// StructuralTools is every tool AuthorizeStructural gates — the domain the
// backfill migration writes folder-scoped grants for. Kept in sync with the
// switch in policy.go (the equivalence oracle covers a representative subset;
// this is the full set the backfill iterates).
var StructuralTools = []string{
	"list_tasks", "inspect_tasks",
	"send", "send_file", "send_voice", "reply", "post", "like", "dislike",
	"delete", "edit", "forward", "quote", "repost",
	"pin_message", "unpin_message", "unpin_all", "pane_set_prompts", "pane_set_title",
	"reset_session", "fork_topic", "inject_message",
	"register_group", "escalate_group", "delegate_group",
	"list_routes", "set_routes", "add_route", "delete_route",
	"network_allow", "network_deny", "network_list",
	"schedule_task", "pause_task", "resume_task", "cancel_task",
	"set_group_open", "set_observe_window", "observe_group", "unobserve_group",
	"get_grants", "set_grants", "list_acl",
	"invite_create", "invite_revoke", "add_acl", "remove_acl",
}

// OutboundVerbs are the tools whose containment is authorizeOutbound's
// self-or-descendant on the TARGET CHAT folder — uniform across all of them and
// INDEPENDENT of magnitude (which platform / whether the folder can send at all is
// a separate jid-param grant). The backfill writes their F/** containment
// unconditionally, because the platform-only verbs (post/like/…) are jid-param
// scoped in the bundle and so are invisible to the nil-param intersection gate.
var OutboundVerbs = map[string]bool{
	"send": true, "send_file": true, "send_voice": true, "reply": true,
	"post": true, "like": true, "dislike": true, "delete": true, "edit": true,
	"forward": true, "quote": true, "repost": true,
	"pin_message": true, "unpin_message": true, "unpin_all": true,
	"pane_set_prompts": true, "pane_set_title": true,
}

// ancestorScopes returns exact-match scopes for each strict ancestor of folder
// ("a/b/c" → ["a", "a/b"]) — the parent-chain half of observe_group's tier-2 rule.
func ancestorScopes(folder string) []string {
	var out []string
	for i := 0; i < len(folder); i++ {
		if folder[i] == '/' {
			out = append(out, folder[:i])
		}
	}
	return out
}

// ScopesMatch reports whether any scope in scopes covers target (matchPattern).
// The runtime containment check once backfill lands; also the oracle's assertion.
func ScopesMatch(scopes []string, target string) bool {
	for _, s := range scopes {
		if matchPattern(s, target) {
			return true
		}
	}
	return false
}
