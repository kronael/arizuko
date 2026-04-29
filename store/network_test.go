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
