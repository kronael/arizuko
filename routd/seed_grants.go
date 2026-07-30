package routd

// seed_grants.go is the 4/R strangler-fig bridge: translate the tier-derived rule
// bundle (grants.DeriveRules) into acl rows on the agent's folder:<path> principal,
// so a folder's grants can be SOURCED FROM acl instead of derived from depth. It is
// additive — not yet wired into spawn. The differential test (seed_grants_test.go)
// proves the acl-sourced decision reproduces the DeriveRules decision exactly; only
// once that is green does the live flip (delete the DeriveRules call) land.

import (
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/store"
)

// SeedFolderGrants writes DeriveRules(folder, tier) as acl rows for
// folder:<folder>. Each rule string ("reply", "send(jid=telegram:*)", "!set_grants",
// "share_mount(readonly=false)") becomes one acl row action=mcp:<verb>, params=<the
// (…) body>, effect=allow|deny — the exact inverse of deriveFolderGrants' overlay
// rendering, so it round-trips.
func SeedFolderGrants(st *store.Store, folder string, tier int, src grants.RouteSource) error {
	for _, rule := range grants.DeriveRules(src, folder, tier, auth.WorldOf(folder)) {
		verb, params, deny := parseRule(rule)
		effect := "allow"
		if deny {
			effect = "deny"
		}
		if err := st.PutACLRow(core.ACLRow{
			Principal: "folder:" + folder,
			Action:    "mcp:" + verb,
			Scope:     folder,
			Params:    params,
			Effect:    effect,
		}); err != nil {
			return err
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
	var rules []string
	for _, r := range st.ACLRowsFor(principals) {
		if !strings.HasPrefix(r.Action, "mcp:") {
			continue
		}
		rule := strings.TrimPrefix(r.Action, "mcp:")
		if r.Params != "" {
			rule += "(" + r.Params + ")"
		}
		if r.Effect == "deny" {
			rule = "!" + rule
		}
		rules = append(rules, rule)
	}
	return rules
}
