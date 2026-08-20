package store

import (
	"reflect"
	"sort"
	"testing"
)

func TestNetworkRulesAddRemoveList(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer s.Close()

	if err := s.AddNetworkRule("acme/eng", "github.com", "alice"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AddNetworkRule("acme/eng", "npmjs.org", "alice"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AddNetworkRule("acme/eng", "github.com", "bob"); err != nil {
		t.Fatalf("Add (dup): %v", err)
	}

	rules, err := s.ListNetworkRules("acme/eng")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rules) != 2 {
		t.Fatalf("want 2 rules (dup ignored), got %d: %+v", len(rules), rules)
	}

	if err := s.RemoveNetworkRule("acme/eng", "github.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	rules, _ = s.ListNetworkRules("acme/eng")
	if len(rules) != 1 || rules[0].Target != "npmjs.org" {
		t.Fatalf("after remove: %+v", rules)
	}
}

// TestAddNetworkRuleValidates: the CLI write path had NO validation — a URL,
// a host:port or an IP was stored and silently never matched (spec 6/25 1b).
// Now the rule is refused where the operator writes it, with the reason; `*`
// stays writable as the folder's allow-all row (live rows exist on krons and
// marinade), and a *.glob lands normalized so deny-by-same-syntax works.
func TestAddNetworkRuleValidates(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer s.Close()

	for _, target := range []string{
		"https://example.com", "example.com:443", "example.com/path",
		"user@example.com", "10.0.0.8", "10.0.0.0/8", "api.*.example.com", "",
	} {
		if err := s.AddNetworkRule("acme", target, "cli"); err == nil {
			t.Errorf("AddNetworkRule(%q) accepted a rule that can never match", target)
		}
	}
	if rules, _ := s.ListNetworkRules("acme"); len(rules) != 0 {
		t.Fatalf("rejected targets must not persist: %+v", rules)
	}

	if err := s.AddNetworkRule("acme", "*", "cli"); err != nil {
		t.Fatalf("the * allow-all row must stay writable by the operator: %v", err)
	}
	if err := s.AddNetworkRule("acme", "*.Api.Example.com.", "cli"); err != nil {
		t.Fatalf("a *.glob is a valid rule: %v", err)
	}
	rules, _ := s.ListNetworkRules("acme")
	if len(rules) != 2 || rules[0].Target != "*" || rules[1].Target != "api.example.com" {
		t.Fatalf("want [* api.example.com] stored normalized, got %+v", rules)
	}

	// Remove by the glob syntax the operator used to add it.
	if err := s.RemoveNetworkRule("acme", "*.api.example.com"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if rules, _ := s.ListNetworkRules("acme"); len(rules) != 1 || rules[0].Target != "*" {
		t.Fatalf("glob-syntax remove must hit the normalized row: %+v", rules)
	}
}

func TestResolveAllowlistAncestry(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer s.Close()

	// root defaults from migration: anthropic.com, api.anthropic.com
	if err := s.AddNetworkRule("acme", "github.com", "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AddNetworkRule("acme/eng", "npmjs.org", "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AddNetworkRule("acme/eng/sre", "pagerduty.com", "test"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	tests := []struct {
		folder string
		want   []string
	}{
		{
			folder: "",
			want:   []string{"anthropic.com", "api.anthropic.com"},
		},
		{
			folder: "acme",
			want:   []string{"anthropic.com", "api.anthropic.com", "github.com"},
		},
		{
			folder: "acme/eng",
			want:   []string{"anthropic.com", "api.anthropic.com", "github.com", "npmjs.org"},
		},
		{
			folder: "acme/eng/sre",
			want:   []string{"anthropic.com", "api.anthropic.com", "github.com", "npmjs.org", "pagerduty.com"},
		},
	}

	for _, tc := range tests {
		got, err := s.ResolveAllowlist(tc.folder)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tc.folder, err)
		}
		sort.Strings(got)
		sort.Strings(tc.want)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("Resolve(%q):\n got  %v\n want %v", tc.folder, got, tc.want)
		}
	}
}

func TestResolveAllowlistDedup(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer s.Close()

	// same target at multiple levels — must dedupe
	if err := s.AddNetworkRule("acme", "github.com", "x"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := s.AddNetworkRule("acme/eng", "github.com", "x"); err != nil {
		t.Fatalf("Add: %v", err)
	}

	got, err := s.ResolveAllowlist("acme/eng")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	count := 0
	for _, t := range got {
		if t == "github.com" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("github.com appears %d times, want 1: %v", count, got)
	}
}

func TestFolderAncestry(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"", []string{""}},
		{"a", []string{"", "a"}},
		{"a/b", []string{"", "a", "a/b"}},
		{"a/b/c/d", []string{"", "a", "a/b", "a/b/c", "a/b/c/d"}},
	}
	for _, tc := range tests {
		got := folderAncestry(tc.in)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("folderAncestry(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
