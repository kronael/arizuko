package main

import (
	"slices"
	"testing"
)

// service:runed brokers per-turn agent tokens by downscoping its OWN service
// token, so its grant must cover the agent capability ceiling (runed
// ManagerConfig.Scopes / routd RunScopes) — else every agent turn 403s on
// downscope (scope_exceeds_parent). Regression for the split cutover.
func TestServiceRunedBrokerCeiling(t *testing.T) {
	g := serviceGrants["service:runed"]
	if len(g) == 0 {
		t.Fatal("service:runed has no grant — every agent turn would 403 on downscope")
	}
	for _, need := range []string{"messages:send:own_group", "chats:read:own_group"} {
		if !slices.Contains(g, need) {
			t.Fatalf("service:runed grant missing %q", need)
		}
	}
}
