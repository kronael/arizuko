package auth

// Scope matching. A scope is "resource:verb". Authorization is scope-match;
// there is no tier (specs/5/1, U-genericization "Capability-vs-tier").
//
// Wildcards: namespace-only. "tasks:*" grants any task verb. There is no
// global "*:*" — operators carry the enumerated resource list
// (specs/5/5-uniform-mcp-rest § scopes).

import "strings"

// HasScope reports whether scope authorizes verb on resource.
func HasScope(scope []string, resource, verb string) bool {
	want := resource + ":" + verb
	for _, s := range scope {
		if scopeMatches(s, want) {
			return true
		}
	}
	return false
}

// MatchesAudience reports whether sub's audience matches aud. Empty sub
// audience matches any (token not bound to one app); empty aud always matches.
func MatchesAudience(sub Subject, aud string) bool {
	return aud == "" || sub.Aud == "" || sub.Aud == aud
}

// scopeMatches reports whether a held scope grants a wanted "resource:verb".
// "tasks:*" matches "tasks:read". An exact string matches itself. "*:*" is
// rejected — never a valid held scope.
func scopeMatches(held, want string) bool {
	if held == "*:*" || held == "*" {
		return false
	}
	if held == want {
		return true
	}
	hr, hv, ok := splitScope(held)
	if !ok || hv != "*" {
		return false
	}
	wr, _, ok := splitScope(want)
	return ok && hr == wr
}

// HasScopeCoveredBy reports whether the single scope `want` is granted by some
// scope in `parent` — the downscope/issuer-mint subset check. A "tasks:*"
// parent covers "tasks:read". authd calls this to bound a minted token.
func HasScopeCoveredBy(parent []string, want string) bool {
	return scopeCoveredBy(parent, want)
}

// scopeCoveredBy reports whether want is granted by some scope in parent —
// used to enforce downscope subset. A "tasks:*" parent covers "tasks:read".
func scopeCoveredBy(parent []string, want string) bool {
	for _, p := range parent {
		if p == want {
			return true
		}
		if scopeMatches(p, want) {
			return true
		}
	}
	return false
}

// intersectScopes returns the requested scopes that are covered by grants,
// preserving requested order and dropping duplicates.
func intersectScopes(requested, grants []string) []string {
	var out []string
	seen := map[string]struct{}{}
	for _, r := range requested {
		if _, dup := seen[r]; dup {
			continue
		}
		if scopeCoveredBy(grants, r) {
			out = append(out, r)
			seen[r] = struct{}{}
		}
	}
	return out
}

func splitScope(s string) (resource, verb string, ok bool) {
	i := strings.IndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}
