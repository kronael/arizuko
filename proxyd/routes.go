package main

import "strings"

// validAuth is the set of accepted `auth` values for a Route. `operator`
// today resolves to `user` plus a daemon-side grant check; capability
// tokens (specs/5/1-auth-standalone.md) will distinguish later. `optional`
// identifies the caller when they offer a credential and forwards anonymously
// otherwise — the route-token surface, where the token is the credential and a
// stamped sub only decides attribution (specs/6/22 step 1).
var validAuth = map[string]bool{"public": true, "user": true, "operator": true, "optional": true}

// Route is one row of proxyd_routes in memory. `arizuko generate` writes the
// release's routes into that table (compose.collectProxydRoutes → store.
// SyncProxydRoutes) and the /v1/proxyd_routes surface writes the operator's;
// proxyd only ever reads. See specs/5/7-proxyd-standalone.md.
type Route struct {
	Path    string `json:"path"`
	Backend string `json:"backend"`
	Auth    string `json:"auth"` // one of validAuth
	// GatedBy is metadata used by generate when selecting routes to write.
	// proxyd does not evaluate it at runtime; Auth gates request authorization.
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
	// RedirectTo non-empty: 302 to this URL instead of proxying. Backend must be "".
	RedirectTo string `json:"redirect_to,omitempty"`
	// Owner says who may rewrite this row: store.RouteOwnerArizuko (generate
	// rewrites it every run) or store.RouteOwnerOperator (generate never touches
	// it). Read-only on the wire — the writer stamps it, never the caller.
	Owner string `json:"owner,omitempty"`
}

// MatchRoute selects the route whose Path is the longest prefix of `path`.
// Trailing-slash paths match as prefixes; bare paths require an exact match.
// Returns nil if no route matches.
func MatchRoute(routes []Route, path string) *Route {
	var best *Route
	bestLen := -1
	for i := range routes {
		r := &routes[i]
		if strings.HasSuffix(r.Path, "/") {
			if strings.HasPrefix(path, r.Path) && len(r.Path) > bestLen {
				best = r
				bestLen = len(r.Path)
			}
		} else if path == r.Path && len(r.Path) > bestLen {
			best = r
			bestLen = len(r.Path)
		}
	}
	return best
}
