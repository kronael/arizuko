package routd

// seed_grants.go is the 4/R grant-surface flip's role layer: it seeds the tier
// bundles onto role:tier<N> principals (SeedTierRoles, at DB open) and renders a
// folder's grants from the acl/role graph (folderGrantsFromACLOnly). deriveFolderGrants
// (mcp.go) assigns a folder its role once and reads from here — grants sourced from
// roles, not depth. The differential test drives the real deriveFolderGrants and proves
// it equals the old DeriveRules.

import (
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/store"
)

// tierRoleName is the seeded role carrying old tier N's non-platform grant bundle.
// The role-based grant-surface flip (4/R) binds folder:<path> → this role by
// membership; deriveFolderGrants then sources the bundle via role expansion
// (grants decoupled from depth) instead of per-folder DeriveRules. Platform verbs
// stay computed per-folder from the world's routed jids (world-derived, not tier).
func tierRoleName(tier int) string {
	return "role:tier" + string(rune('0'+tier))
}

// SeedTierRoles writes the four tier bundles (non-platform: basicSend + tierN-fixed
// + share_mount, or "*" for tier 0) as acl rows on role:tier0..3, scope **. Uses a
// nil RouteSource so platform rules are excluded (they are per-world, added by
// deriveFolderGrants). Idempotent (PutACLRow = INSERT OR IGNORE); seeded once, not
// per folder — so list_acl on a folder stays clean (role rows live on the role
// principal). A folder gains the bundle by an acl_membership edge to its role.
func SeedTierRoles(st *store.Store) error {
	for tier := 0; tier <= 3; tier++ {
		principal := tierRoleName(tier)
		want := tierRoleRows(principal, tier)
		// Skip-if-unchanged: SeedTierRoles runs at EVERY routd.Open (daemon restart,
		// and every `arizuko packages` against a running instance). Reseeding
		// unconditionally would (a) churn — delete+reinsert ~40 rows each open — and
		// (b) leave the principal momentarily empty, so a concurrent turn resolving
		// its grants sees none. Only a genuine bundle change (a deploy) rewrites rows,
		// and that rewrite is atomic (ReplaceACLPrincipalRows), never a bare window.
		if aclRowsEqual(st.ListACL(principal), want) {
			continue
		}
		if err := st.ReplaceACLPrincipalRows(principal, want); err != nil {
			return err
		}
	}
	return nil
}

// tierRoleRows renders tier N's non-platform bundle as acl rows on the role
// principal (scope **). Prune-and-reseed semantics live in ReplaceACLPrincipalRows;
// this is the pure desired set (also used for change detection).
func tierRoleRows(principal string, tier int) []core.ACLRow {
	var rows []core.ACLRow
	for _, rule := range grants.DeriveRules(nil, "", tier, "") {
		verb, params, deny := parseRule(rule)
		effect := "allow"
		if deny {
			effect = "deny"
		}
		rows = append(rows, core.ACLRow{
			Principal: principal,
			Action:    "mcp:" + verb,
			Scope:     "**",
			Params:    params,
			Effect:    effect,
			// A group may re-delegate a grant it holds to a child (4/R lineage
			// delegation) — allow rows carry the grant option; denies (restrictions)
			// do not. The subset-check (auth.Delegate) bounds what can be passed.
			GrantOption: !deny,
		})
	}
	return rows
}

// aclRowsEqual compares two acl row sets on the semantic fields only (principal,
// action, scope, params, predicate, effect, grant_option) — NOT granted_at/by,
// which differ every seed. Order-independent. Used to skip a no-op reseed.
func aclRowsEqual(a, b []core.ACLRow) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(r core.ACLRow) string {
		g := "0"
		if r.GrantOption {
			g = "1"
		}
		return strings.Join([]string{r.Principal, r.Action, r.Scope, r.Params, r.Predicate, r.Effect, g}, "\x00")
	}
	seen := make(map[string]int, len(a))
	for _, r := range a {
		seen[key(r)]++
	}
	for _, r := range b {
		seen[key(r)]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// parseRule splits a DeriveRules rule into (verb, params, deny). Formats:
// "verb", "verb(params)", "!verb", "!verb(params)".
func parseRule(rule string) (verb, params string, deny bool) {
	if strings.HasPrefix(rule, "!") {
		deny = true
		rule = rule[1:]
	}
	if i := strings.IndexByte(rule, '('); i >= 0 && strings.HasSuffix(rule, ")") {
		return rule[:i], rule[i+1 : len(rule)-1], deny
	}
	return rule, "", deny
}

// folderGrantsFromACLOnly renders the folder's grant rules from acl ALONE (the
// expanded overlay, no DeriveRules base) — the post-flip source. The differential
// test compares CheckAction over this against CheckAction over DeriveRules.
func folderGrantsFromACLOnly(st *store.Store, folder string) []string {
	principals := append([]string{"folder:" + folder}, st.Ancestors("folder:"+folder)...)
	// Deny-precedence is order-dependent: grants.CheckAction is last-match-wins, and
	// store.ACLRowsFor has no ORDER BY (the index returns folder: before role:,
	// lexicographically). So emitting rows in query order let a folder: DENY sort
	// BEFORE a role: ALLOW and get masked (adversary BUG 3). Partition allows-then-
	// denies so every deny sorts LAST — deny now wins regardless of row/principal
	// order, matching the deny-wins the acl evaluator (auth.AuthorizeWith) uses.
	var allows, denies []string
	for _, r := range st.ACLRowsFor(principals) {
		if !strings.HasPrefix(r.Action, "mcp:") {
			continue
		}
		rule := strings.TrimPrefix(r.Action, "mcp:")
		if r.Params != "" {
			rule += "(" + r.Params + ")"
		}
		if r.Effect == "deny" {
			denies = append(denies, "!"+rule)
			continue
		}
		allows = append(allows, rule)
	}
	return append(allows, denies...)
}
