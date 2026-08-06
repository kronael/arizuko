package main

import (
	"slices"
	"testing"
)

// Every daemon that presents AUTHD_SERVICE_KEY needs a serviceGrants entry;
// without one it is minted with EMPTY scope and its calls 403 silently. That has
// now shipped five times — the channel adapters (split A1), runed's broker
// ceiling, webd's form submissions, and dashd's kill button (BUGS F15a).
//
// This test pins the invariant for the whole map rather than for dashd alone, so
// the sixth occurrence fails here instead of in production: a principal that is
// declared must carry at least one scope.
func TestEveryServiceGrantIsNonEmpty(t *testing.T) {
	if len(serviceGrants) == 0 {
		t.Fatal("serviceGrants is empty — no principal could authenticate")
	}
	for principal, scopes := range serviceGrants {
		if len(scopes) == 0 {
			t.Errorf("%s has an empty grant — its calls will 403 with no error at the call site", principal)
		}
	}
}

// dashd proxies the operator UI's mutations rather than writing other daemons'
// tables, and reads what it renders. Two scopes, each pinned as NECESSARY:
//
//   - runs:kill — POST /v1/runs/stop (runed/server.go), the /dash/runed/ kill
//     button. The whapd pair endpoints are not scope-gated and proxyd authorizes
//     the FORWARDED operator identity, not dashd's scope, so neither adds one.
//   - routes:read — GET /v1/engagement (routd/reads_http.go scopeRoutesRead), the
//     /dash/engagement/ view (spec 5/G item 6, BUGS F31).
//
// The upper bound is the half that matters, so it is asserted rather than
// implied. routes:write is on the too-wide list deliberately: /dash/engagement/
// is a VIEW. Adding a disengage control is the change that earns routes:write,
// and it also owes an audit row in routd's transaction — so a grant carrying
// routes:write while no such control exists is drift, and fails here.
// secrets:read is listed because it is the one READ scope that would turn this
// ceiling into a leak; routes:read cannot, its widest read (ListRouteTokens)
// never selects the token value.
func TestServiceDashdIsScopedToWhatItProxiesAndReads(t *testing.T) {
	g := serviceGrants["service:dashd"]
	if len(g) == 0 {
		t.Fatal("service:dashd has no grant — /dash/runed/'s kill button 403s")
	}
	for _, needed := range []string{"runs:kill", "routes:read"} {
		if !slices.Contains(g, needed) {
			t.Errorf("service:dashd grant missing %q, got %v", needed, g)
		}
	}
	for _, tooWide := range []string{
		"*", "runs:run", "messages:write", "grants:read", "routes:write", "secrets:read",
	} {
		if slices.Contains(g, tooWide) {
			t.Errorf("service:dashd holds %q — the operator UI proxies and reads, "+
				"it does not originate work", tooWide)
		}
	}
}
