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

// dashd proxies the operator UI's actions rather than writing other daemons'
// tables, and reads what it renders. Three scopes, each pinned as NECESSARY:
//
//   - runs:kill — POST /v1/runs/stop (runed/server.go), the /dash/runed/ kill
//     button. The whapd pair endpoints are not scope-gated and proxyd authorizes
//     the FORWARDED operator identity, not dashd's scope, so neither adds one.
//   - routes:read — GET /v1/engagement (routd/reads_http.go scopeRoutesRead), the
//     /dash/engagement/ view (spec 5/G item 6, BUGS F31).
//   - routes:write — POST /v1/engagement (scopeRoutesWrite), the same page's
//     force-disengage control. It was pinned as TOO WIDE until that control
//     existed, on two stated conditions: the mutation must land an audit row
//     inside routd's own transaction, and the write path must contain on the
//     window's claiming folder rather than only on the jid's route target. Both
//     shipped (routd DB.SetEngagementAudited; handleEngagementSet's ownsFolder
//     check on the live window's owner), so the scope is earned rather than
//     merely convenient.
//
// The upper bound is the half that matters, so it is asserted TWICE and neither
// half was weakened to let routes:write in.
//
//   - By NAME, for the message: each listed scope names a distinct way the
//     ceiling could stop being "proxies and reads" — originating work
//     (runs:run), speaking as a channel (messages:write), reading credentials
//     (grants:read, secrets:read), or holding everything (*). secrets:read is
//     the one READ that would turn this into a leak; routes:read cannot, its
//     widest read (ListRouteTokens) never selects the token value.
//   - By COUNT, which is the stronger bound and closes the gap that letting
//     routes:write off the named list would otherwise open: the grant is exactly
//     these three, so a FOURTH scope of ANY name — including one nobody thought
//     to blacklist — fails here before it ships.
func TestServiceDashdIsScopedToWhatItProxiesAndReads(t *testing.T) {
	g := serviceGrants["service:dashd"]
	if len(g) == 0 {
		t.Fatal("service:dashd has no grant — /dash/runed/'s kill button 403s")
	}
	want := []string{"runs:kill", "routes:read", "routes:write"}
	for _, needed := range want {
		if !slices.Contains(g, needed) {
			t.Errorf("service:dashd grant missing %q, got %v", needed, g)
		}
	}
	if len(g) != len(want) {
		t.Errorf("service:dashd holds %d scopes %v, want exactly %v — the operator UI "+
			"proxies and reads; every scope beyond these needs the sign-off routes:write got",
			len(g), g, want)
	}
	for _, tooWide := range []string{
		"*", "runs:run", "messages:write", "grants:read", "secrets:read",
	} {
		if slices.Contains(g, tooWide) {
			t.Errorf("service:dashd holds %q — the operator UI proxies and reads, "+
				"it does not originate work", tooWide)
		}
	}
}
