package main

import (
	"slices"
	"testing"
)

// Channel adapters post inbound to routd's /v1/messages (messages:write). Each
// exchanges its AUTHD_SERVICE_KEY for a service:<adapter> JWT whose scope is the
// principal's declared serviceGrants — missing the entry → empty scope → every
// inbound 401/403s (the split's A1). Regression for the split cutover.
func TestServiceAdapterGrants(t *testing.T) {
	adapters := []string{
		"teled", "whapd", "discd", "mastd", "slakd",
		"bskyd", "reditd", "emaid", "twitd", "linkd",
	}
	for _, a := range adapters {
		principal := "service:" + a
		g, ok := serviceGrants[principal]
		if !ok || len(g) == 0 {
			t.Errorf("%s has no service grant — every inbound would 401/403", principal)
			continue
		}
		if !slices.Contains(g, "messages:write") {
			t.Errorf("%s grant missing messages:write, got %v", principal, g)
		}
	}
}

// webd is web ingress (route-token /chat + /hook), not a channel adapter, but it
// posts inbound to /v1/messages the same way — so it needs messages:write too.
// Missing it made every strengths-form submission 403 → "router unavailable" 502.
func TestServiceWebdGrant(t *testing.T) {
	g, ok := serviceGrants["service:webd"]
	if !ok || !slices.Contains(g, "messages:write") {
		t.Errorf("service:webd missing messages:write grant, got %v (ok=%v)", g, ok)
	}
}
