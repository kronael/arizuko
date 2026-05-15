package auth

import (
	"strings"

	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/store"
)

// Caller is the bearer of a request. Principal must be canonical
// (post-CanonicalSub for OAuth subs). Claims carry JWT claims used by
// row predicates.
type Caller struct {
	Principal string
	Claims    map[string]string
	// Extra principals to fold into the expansion set without a DB lookup,
	// e.g. the room JID for channel-bot inbound (spec 6/9 §Membership).
	Extra []string
}

// AuthorizeOpts adjusts tier-default fallback for mcp:* requests.
// Both Folder + WorldFolder must be set to enable fallback; otherwise
// fallback is skipped (no match -> deny).
type AuthorizeOpts struct {
	Folder      string
	WorldFolder string
	Tier        int
}

// Authorize returns true iff caller is permitted to perform action on
// scope. Spec 6/9 §Authorize: row-based grants with deny-wins and
// tier-default fallback for mcp:* actions. The orthogonal structural
// concern (tree-shape invariants, tier bounds, task-owner) lives in
// AuthorizeStructural (policy.go). Many tool callsites need both.
//
// Deny wins. No match: for mcp:<tool> actions, fall back to tier
// defaults via grants.DeriveRules; for interact/admin/*, deny.
func Authorize(
	s *store.Store,
	caller Caller,
	action, scope string,
	params map[string]string,
) bool {
	return AuthorizeWith(s, caller, action, scope, params, AuthorizeOpts{})
}

// AuthorizeWith is Authorize with explicit tier-default fallback config.
func AuthorizeWith(
	s *store.Store,
	caller Caller,
	action, scope string,
	params map[string]string,
	opts AuthorizeOpts,
) bool {
	if s == nil || caller.Principal == "" || action == "" {
		return false
	}

	// 1. Expand principal set.
	expanded := expandPrincipals(s, caller)

	// 2. Exact-match rows.
	rows := s.ACLRowsFor(expanded)

	// 3. Wildcard rows: stored principal contains '*'. Filter against
	// expanded set with segment-wise glob.
	for _, r := range s.ACLWildcardRows() {
		if anyPrincipalMatches(r.Principal, expanded) {
			rows = append(rows, r)
		}
	}

	// 4. Evaluate.
	allowed, denied := false, false
	for _, r := range rows {
		if !actionCovers(r.Action, action) {
			continue
		}
		if !matchPattern(r.Scope, scope) {
			continue
		}
		if !predicateMatches(r.Predicate, caller.Claims) {
			continue
		}
		if !paramsMatch(r.Params, params) {
			continue
		}
		if r.Effect == "deny" {
			denied = true
		} else {
			allowed = true
		}
	}
	if denied {
		return false
	}
	if allowed {
		return true
	}

	// 5. Tier-default fallback for mcp:* only.
	if !strings.HasPrefix(action, "mcp:") {
		return false
	}
	if opts.Folder == "" || opts.WorldFolder == "" {
		return false
	}
	if !matchPattern(opts.Folder, scope) && opts.Folder != scope {
		// Tier defaults apply only at the agent's own folder.
		return false
	}
	tool := strings.TrimPrefix(action, "mcp:")
	rules := grants.DeriveRules(s, opts.Folder, opts.Tier, opts.WorldFolder)
	return grants.CheckAction(rules, tool, params)
}

// expandPrincipals: caller.Principal + caller.Extra plus the transitive
// ancestors of each via acl_membership. Deduplicated.
func expandPrincipals(s *store.Store, caller Caller) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(caller.Principal)
	for _, p := range caller.Extra {
		add(p)
	}
	// Start a frontier; expand each new principal's ancestors. Ancestors
	// is itself transitive but is keyed off a single child — fold them.
	frontier := append([]string{caller.Principal}, caller.Extra...)
	for _, p := range frontier {
		for _, anc := range s.Ancestors(p) {
			add(anc)
		}
	}
	return out
}

// anyPrincipalMatches: row.Principal contains a glob; does it match any
// element of expanded? Uses segment-wise globbing on ':' AND '/'.
func anyPrincipalMatches(pattern string, expanded []string) bool {
	for _, p := range expanded {
		if matchPrincipal(pattern, p) {
			return true
		}
	}
	return false
}

// matchPrincipal: segment-wise glob on both ':' and '/'. `**` crosses
// segments; `*` does not. Implemented by chunking on ':' first, then on
// '/' within each chunk.
func matchPrincipal(pattern, p string) bool {
	if pattern == p {
		return true
	}
	if pattern == "**" {
		return true
	}
	// Split on ':' once: namespace : rest.
	patNs, patRest := splitNs(pattern)
	pNs, pRest := splitNs(p)
	if !segGlob(patNs, pNs) {
		return false
	}
	// rest is '/' separated; reuse matchPattern.
	if patRest == "" && pRest == "" {
		return true
	}
	return matchPattern(patRest, pRest)
}

func splitNs(s string) (ns, rest string) {
	if i := strings.IndexByte(s, ':'); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, ""
}

// segGlob: glob across a single segment (no '/' or ':' crossing). `*`
// matches any chars, `**` matches like `*` here (no boundary to cross).
func segGlob(pat, s string) bool {
	if pat == "*" || pat == "**" {
		return true
	}
	if pat == s {
		return true
	}
	// Fall through to path.Match-like with no separators in segment.
	for {
		if pat == "" {
			return s == ""
		}
		if pat[0] == '*' {
			pat = strings.TrimLeft(pat, "*")
			for i := 0; i <= len(s); i++ {
				if segGlob(pat, s[i:]) {
					return true
				}
			}
			return false
		}
		if s == "" || pat[0] != s[0] {
			return false
		}
		pat = pat[1:]
		s = s[1:]
	}
}

// actionCovers: does a stored row action cover the requested action?
// Lattice: `*` ⊃ admin ⊃ interact; `*` ⊃ mcp:<tool>; admin ⊃ mcp:<tool>;
// `mcp:*` ⊃ mcp:<tool>.
func actionCovers(rowAction, requested string) bool {
	if rowAction == "*" {
		return true
	}
	if rowAction == requested {
		return true
	}
	switch rowAction {
	case "admin":
		return requested == "interact" || strings.HasPrefix(requested, "mcp:")
	case "mcp:*":
		return strings.HasPrefix(requested, "mcp:")
	}
	return false
}

// predicateMatches: empty predicate = no claim required. Non-empty has
// the form `key=glob`; the claim at `key` must glob-match `glob`. Only
// one conjunction supported (spec open question 1).
func predicateMatches(predicate string, claims map[string]string) bool {
	if predicate == "" {
		return true
	}
	for _, part := range strings.Split(predicate, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			// Bare key: claim must be present and non-empty.
			if claims[part] == "" {
				return false
			}
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		got, ok := claims[k]
		if !ok {
			return false
		}
		if !segGlob(v, got) {
			return false
		}
	}
	return true
}

// paramsMatch: empty params = no constraint. Non-empty has the form
// `key=glob[,key=glob]`; every key must be present in passed params and
// glob-match.
func paramsMatch(paramSpec string, params map[string]string) bool {
	if paramSpec == "" {
		return true
	}
	for _, part := range strings.Split(paramSpec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			if _, ok := params[part]; !ok {
				return false
			}
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		got, ok := params[k]
		if !ok {
			return false
		}
		if !segGlob(v, got) {
			return false
		}
	}
	return true
}
