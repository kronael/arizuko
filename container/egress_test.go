package container

import (
	"slices"
	"testing"
)

// TestGatewayHostJoinsAllowlist pins the rule that a configured gateway is
// reachable by construction. Without it the per-spawn proxy denies the gateway
// and every turn fails with no model call, on an instance that looks correctly
// configured.
func TestGatewayHostJoinsAllowlist(t *testing.T) {
	for _, tc := range []struct {
		name, url, want string
	}{
		{"full URL", "http://litellm:4000", "litellm"},
		{"https with path", "https://gw.example.com/v1", "gw.example.com"},
		{"bare host:port", "litellm:4000", "litellm"},
		{"unset", "", ""},
		{"unparseable", "://nope", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gatewayHost(tc.url); got != tc.want {
				t.Errorf("gatewayHost(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestEgressRulesSeparatesAllowAllFromTheNames pins the translation every
// substrate reads: `*` becomes the proxy's explicit --allow-all mode and never a
// name in the list, and the gateway is appended once.
func TestEgressRulesSeparatesAllowAllFromTheNames(t *testing.T) {
	names, allowAll := EgressRules([]string{"github.com", "*", "pypi.org"}, "http://litellm:4000")
	if !allowAll {
		t.Error("a `*` entry must set allow-all: the proxy takes a mode, not a magic name")
	}
	want := []string{"github.com", "pypi.org", "litellm"}
	if !slices.Equal(names, want) {
		t.Errorf("names = %v, want %v", names, want)
	}

	names, allowAll = EgressRules([]string{"github.com"}, "")
	if allowAll {
		t.Error("no `*` entry must leave allow-all off")
	}
	if !slices.Equal(names, []string{"github.com"}) {
		t.Errorf("names = %v, want just the one name", names)
	}
}
