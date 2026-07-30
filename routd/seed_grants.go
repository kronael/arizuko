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
		// Prune first so a TIGHTENED bundle actually revokes (adversary BUG 4:
		// INSERT OR IGNORE alone never deletes a verb pulled from a tier). Membership
		// edges (folder→role) are untouched; only the role's grant rows are rebuilt.
		if err := st.DeleteACLPrincipal(tierRoleName(tier)); err != nil {
			return err
		}
		for _, rule := range grants.DeriveRules(nil, "", tier, "") {
			verb, params, deny := parseRule(rule)
			effect := "allow"
			if deny {
				effect = "deny"
			}
			if err := st.PutACLRow(core.ACLRow{
				Principal: tierRoleName(tier),
				Action:    "mcp:" + verb,
				Scope:     "**",
				Params:    params,
				Effect:    effect,
				// A group may re-delegate a grant it holds to a child (4/R lineage
				// delegation) — allow rows carry the grant option; denies (restrictions)
				// do not. The subset-check (auth.Delegate) bounds what can be passed.
				GrantOption: !deny,
			}); err != nil {
				return err
			}
		}
	}
	return nil
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
