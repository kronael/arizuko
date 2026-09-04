package main

import (
	"slices"
	"testing"
)

// Every daemon that presents AUTHD_SERVICE_KEY needs a serviceGrants entry;
// without one it is minted with EMPTY scope and its calls 403 silently. That has
// shipped five times (BUGS F15a). This pins the invariant for the whole map, so
// the sixth occurrence fails here instead of in production.
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

// dashd's grant is pinned two ways, and the second is the stronger:
//
//   - By NAME, so a scope that is silently dropped fails here rather than as a
//     403 on the page it powered.
//   - By COUNT, so a TWELFTH scope of ANY name — including one nobody thought to
//     blacklist — fails before it ships. This bound has caught nine additions and
//     forced each to state its case.
//
// tokens:mint is blacklisted by name. It is the one scope that would turn "can
// end a session" into "can BE anyone" — IssuerMint mints a user token bounded by
// the TARGET's grants, not the caller's — and the count bound alone would have
// let it in as a swap for one of the others.
//
// dead_letters:write rests on being NARROWER than a scope already blacklisted
// here: messages:write composes any inbound; dead_letters:write moves one
// already-composed, already-failed row back to 'pending'. Reusing messages:*
// would have smuggled "speak as a channel" past this list.
func TestServiceDashdIsScopedToWhatItProxiesAndReads(t *testing.T) {
	g := serviceGrants["service:dashd"]
	if len(g) == 0 {
		t.Fatal("service:dashd has no grant — /dash/runed/'s kill button 403s")
	}
	want := []string{
		"runs:kill", "routes:read", "routes:write", "audit:read",
		"signing_keys:read", "sessions:read", "sessions:write",
		"pending_actions:read", "pending_actions:write",
		"dead_letters:read", "dead_letters:write",
		// Added 2026-09-04 for 6/28's read path (BUGS L142). Both READ-ONLY and
		// both replacing a direct routd.db read the pages already did, so the
		// grant narrows dashd's reach rather than widening it. The ceiling below
		// still bites: a third addition fails this test until someone justifies
		// it the same way.
		"tasks:read", "messages:read",
	}
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
		"*", "runs:run", "messages:write", "grants:read", "secrets:read", "tokens:mint",
	} {
		if slices.Contains(g, tooWide) {
			t.Errorf("service:dashd holds %q — the operator UI proxies and reads, "+
				"it does not originate work", tooWide)
		}
	}
}
