package auth

import (
	"slices"
	"strings"

	"github.com/kronael/arizuko/store"
)

// Caller is the bearer of a request. Principal is the JWT subject as authd
// minted it: for a human that is the ACCOUNT's canonical provider sub, already
// resolved at mint from whichever linked login was presented (spec 5/32 §
// Alias resolution). There is no resolve step here — Authorize evaluates the
// principal it is handed, and `acl.principal` rows key on that same value.
// Claims carry JWT claims used by row predicates.
type Caller struct {
	Principal string
	Claims    map[string]string
	// Extra principals to fold into the expansion set without a DB lookup,
	// e.g. the room JID for channel-bot inbound (spec 6/9 §Membership).
	Extra []string
}

// Authorize returns true iff caller is permitted to perform action on scope:
// row-based grants over the caller's expanded principal set, deny-wins. It is the
// SOLE runtime authorization evaluator (5/33): magnitude AND containment in one call
// — a delegated row scoped `acme/**` authorizes `mcp:<tool>` on any target under
// acme. Management callsites pass the ACTUAL target as scope; a no-match is a deny.
func Authorize(
	s *store.Store,
	caller Caller,
	action, scope string,
	params map[string]string,
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
	// A folder's magnitude is its role membership (role:member floor + delegated
	// grants) + operator grants. No fallback: a folder with no matching allow row is
	// denied, loud.
	return allowed
}

// EffectiveActions returns a predicate reporting whether caller holds an allow row
// for an mcp action at ANY scope — the tool-VISIBILITY view (tools/list), orthogonal
// to the per-target authz Authorize does. A lattice grant (`*`, `mcp:*`, `admin`)
// makes every mcp tool visible; an explicit `mcp:<tool>` allow makes that one
// visible. Deny rows are scope-specific and don't hide a tool from the list (the
// per-call Authorize still enforces them). Reads are advertised unconditionally by
// their callers, never through this view.
func EffectiveActions(s *store.Store, caller Caller) func(action string) bool {
	if s == nil || caller.Principal == "" {
		return func(string) bool { return false }
	}
	expanded := expandPrincipals(s, caller)
	rows := s.ACLRowsFor(expanded)
	for _, r := range s.ACLWildcardRows() {
		if anyPrincipalMatches(r.Principal, expanded) {
			rows = append(rows, r)
		}
	}
	lattice := false
	held := map[string]bool{}
	for _, r := range rows {
		if r.Effect == "deny" {
			continue
		}
		switch r.Action {
		case "*", "mcp:*", "admin":
			lattice = true
		default:
			held[r.Action] = true
		}
	}
	return func(action string) bool {
		if lattice {
			return strings.HasPrefix(action, "mcp:")
		}
		return held[action]
	}
}

// UserScopes lists the distinct allow-scope patterns `sub` holds. It is the
// source of the X-User-Groups header proxyd stamps and of authd's login-time
// scope snapshot.
//
// It reads the SAME rows Authorize evaluates — expanded principals plus
// wildcard-principal rows — rather than its own SQL, so it cannot drift from
// the gate about WHICH rows exist. It previously lived in store/ as a raw
// `principal IN (...)` query, which silently missed every wildcard-principal
// grant (`google:*`).
//
// It is a LISTING, and it can never be the decision. A []string carries no
// action and cannot represent a deny row, so a scope appearing here means only
// "some allow row grants it, at some action". Anything gating access MUST call
// Authorize, which reads action, predicate, params and deny-wins; a caller that
// treats this result as a verdict has built a second, weaker evaluator.
func UserScopes(s *store.Store, sub string) []string {
	if s == nil || sub == "" {
		return nil
	}
	expanded := expandPrincipals(s, Caller{Principal: sub})
	rows := s.ACLRowsFor(expanded)
	for _, r := range s.ACLWildcardRows() {
		if anyPrincipalMatches(r.Principal, expanded) {
			rows = append(rows, r)
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		if r.Effect == "deny" || r.Scope == "" || seen[r.Scope] {
			continue
		}
		seen[r.Scope] = true
		out = append(out, r.Scope)
	}
	slices.Sort(out)
	return out
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
	if before, after, ok := strings.Cut(s, ":"); ok {
		return before, after
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
	for part := range strings.SplitSeq(predicate, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		before, after, hasEq := strings.Cut(part, "=")
		if !hasEq {
			// Bare key: claim must be present and non-empty.
			if claims[part] == "" {
				return false
			}
			continue
		}
		k := strings.TrimSpace(before)
		v := strings.TrimSpace(after)
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
	for part := range strings.SplitSeq(paramSpec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		before, after, hasEq := strings.Cut(part, "=")
		if !hasEq {
			if _, ok := params[part]; !ok {
				return false
			}
			continue
		}
		k := strings.TrimSpace(before)
		v := strings.TrimSpace(after)
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

// HoldPrefix namespaces hold rules. A hold is NEVER an `effect` on a plain
// `mcp:<tool>` row: Authorize treats every non-deny effect as ALLOW, so such a
// row would silently GRANT the tool it meant to gate (spec 5/19 Hazard 1).
const HoldPrefix = "hold:mcp:"

// CheckHold answers "must this tool call wait for a human?" — a DIRECT scoped
// read of `hold:mcp:`-prefixed rows, deliberately NOT routed through Authorize.
// Authorize's action lattice has `*` cover everything and role:operator holds
// `(*, **)`, so asking it would hold EVERY tool for the operator (spec 5/19
// Hazard 2). Matching is exact on the action, with `hold:mcp:*` as the only
// wildcard; scope, predicate and params reuse Authorize's own matchers, so
// there is one predicate language, not two.
//
// Deny-wins is not modelled: a hold row is not an authorization, and a row
// saying "do not hold" is just the absence of a row.
func CheckHold(s *store.Store, caller Caller, tool, scope string, params map[string]string) bool {
	if s == nil || caller.Principal == "" || tool == "" {
		return false
	}
	want := HoldPrefix + tool
	expanded := expandPrincipals(s, caller)
	rows := s.ACLRowsFor(expanded)
	for _, r := range s.ACLWildcardRows() {
		if anyPrincipalMatches(r.Principal, expanded) {
			rows = append(rows, r)
		}
	}
	for _, r := range rows {
		if r.Action != want && r.Action != HoldPrefix+"*" {
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
		return true
	}
	return false
}
