package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validAuth is the set of accepted `auth` values for a Route. `operator`
// today resolves to `user` plus a daemon-side grant check; capability
// tokens (specs/6/1-auth-standalone.md) will distinguish later.
var validAuth = map[string]bool{"public": true, "user": true, "operator": true}

// Route is one entry in proxyd's TOML-declared route table. The JSON shape
// matches `[[proxyd_route]]` in `template/services/*.toml`; compose.go
// collects the survivors (after gated_by env evaluation) into
// PROXYD_ROUTES_JSON. See specs/6/2-proxyd-standalone.md.
type Route struct {
	Path            string   `json:"path"`
	Backend         string   `json:"backend"`
	Auth            string   `json:"auth"` // "public" | "user" | "operator"
	GatedBy         string   `json:"gated_by,omitempty"`
	PreserveHeaders []string `json:"preserve_headers,omitempty"`
	StripPrefix     bool     `json:"strip_prefix,omitempty"`
}

// LoadRoutes parses a JSON-encoded route list (typically PROXYD_ROUTES_JSON).
// Empty input → empty slice (no error). Malformed JSON or invalid route → error.
// Validation enforces the contract at the boundary: leading-slash paths, known
// `auth` value, non-empty backend.
func LoadRoutes(raw string) ([]Route, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var routes []Route
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil, err
	}
	seen := make(map[string]bool, len(routes))
	for i, r := range routes {
		if !strings.HasPrefix(r.Path, "/") {
			return nil, fmt.Errorf("route[%d] path %q: must start with '/'", i, r.Path)
		}
		if r.Backend == "" {
			return nil, fmt.Errorf("route[%d] path %q: backend is required", i, r.Path)
		}
		if !validAuth[r.Auth] {
			return nil, fmt.Errorf("route[%d] path %q: auth %q not in {public,user,operator}", i, r.Path, r.Auth)
		}
		if seen[r.Path] {
			return nil, fmt.Errorf("route[%d] path %q: duplicate path", i, r.Path)
		}
		seen[r.Path] = true
	}
	return routes, nil
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
