package main

import (
	"strings"

	"github.com/kronael/arizuko/store"
)

// validAuth is the set of accepted `auth` values for a Route. `operator`
// today resolves to `user` plus a daemon-side grant check. `optional`
// identifies the caller when they offer a credential and forwards anonymously
// otherwise — the route-token surface, where the token is the credential and a
// stamped sub only decides attribution (proxyd/README.md).
var validAuth = map[string]bool{"public": true, "user": true, "operator": true, "optional": true}

// MatchRoute selects the route whose Path is the longest prefix of `path`.
// Trailing-slash paths match as prefixes; bare paths require an exact match.
// Returns nil if no route matches.
func MatchRoute(routes []store.ProxydRoute, path string) *store.ProxydRoute {
	var best *store.ProxydRoute
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
