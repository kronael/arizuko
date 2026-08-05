package auth

import (
	"strings"
	"testing"
)

func TestMatchGroups(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		folder  string
		want    bool
	}{
		{"empty", nil, "alice", false},
		{"double-star nested", []string{"**"}, "pub/a/b", true},
		{"literal match", []string{"alice"}, "alice", true},
		{"literal mismatch", []string{"alice"}, "bob", false},
		{"glob one segment", []string{"pub/*"}, "pub/foo", true},
		{"glob no cross slash", []string{"pub/*"}, "pub/foo/bar", false},
		{"multi entry first", []string{"alice", "pub/*"}, "alice", true},
		{"multi entry second", []string{"alice", "pub/*"}, "pub/x", true},
		{"world doublestar one", []string{"world/**"}, "world/a", true},
		{"world doublestar deep", []string{"world/**"}, "world/a/b/c", true},
		{"world doublestar root", []string{"world/**"}, "world", true},
		{"world doublestar miss", []string{"world/**"}, "other/a", false},
		{"mid doublestar", []string{"w/**/leaf"}, "w/a/b/leaf", true},
		{"mid doublestar no leaf", []string{"w/**/leaf"}, "w/a/b/c", false},
	}
	for _, c := range cases {
		if got := MatchGroups(c.allowed, c.folder); got != c.want {
			t.Errorf("%s: MatchGroups(%v, %q) = %v, want %v",
				c.name, c.allowed, c.folder, got, c.want)
		}
	}
}

// The containment contract of 5/33 decision 8: the scope glob IS the reach.
// A bare folder scope is ONE folder — no implicit parent→child inheritance,
// because a path carries zero authorization (decision 2). Every surface that
// answers "may P touch folder F" answers it here; this test is what breaks if
// any of them grows a prefix walk of its own.
func TestMatchGroupsContainmentIsTheGlob(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		folder  string
		want    bool
	}{
		{"bare scope covers itself", []string{"atlas"}, "atlas", true},
		{"bare scope does NOT reach child", []string{"atlas"}, "atlas/search", false},
		{"bare scope does NOT reach grandchild", []string{"atlas"}, "atlas/search/deep", false},
		{"child scope does NOT reach parent", []string{"atlas/search"}, "atlas", false},
		{"subtree glob reaches child", []string{"atlas/**"}, "atlas/search", true},
		{"subtree glob reaches grandchild", []string{"atlas/**"}, "atlas/search/deep", true},
		{"subtree glob covers the root itself", []string{"atlas/**"}, "atlas", true},
		{"subtree glob stops at the tenant", []string{"atlas/**"}, "atlasx", false},
		{"subtree glob is not a string prefix", []string{"atlas/**"}, "atlas-eng/x", false},
		{"single-star reaches direct child only", []string{"atlas/*"}, "atlas/search", true},
		{"single-star stops at grandchild", []string{"atlas/*"}, "atlas/search/deep", false},
		{"operator grant covers any depth", []string{"**"}, "a/b/c/d/e", true},
		{"operator grant covers top level", []string{"**"}, "atlas", true},
		{"cross-tenant denial", []string{"atlas/**"}, "research/x", false},
	}
	for _, c := range cases {
		if got := MatchGroups(c.allowed, c.folder); got != c.want {
			t.Errorf("%s: MatchGroups(%v, %q) = %v, want %v",
				c.name, c.allowed, c.folder, got, c.want)
		}
	}
}

// A scope that does not parse must deny, not silently match or silently skip.
// path.Match returns ErrBadPattern for an unterminated character class; the old
// code discarded that error and treated it as "no match on this segment",
// which let a malformed pattern read as a deliberate deny.
func TestMatchGroupsMalformedScopeDenies(t *testing.T) {
	for _, p := range []string{"atlas/[", "[a-/search", "**/["} {
		if MatchGroups([]string{p}, "atlas/search") {
			t.Errorf("malformed scope %q must not grant", p)
		}
		if _, err := matchSegments(strings.Split(p, "/"), []string{"atlas", "search"}); err == nil {
			t.Errorf("malformed scope %q must surface a parse error, not swallow it", p)
		}
	}
	// A well-formed neighbour in the same set still grants — one bad row does
	// not fail the whole evaluation closed.
	if !MatchGroups([]string{"atlas/[", "atlas/**"}, "atlas/search") {
		t.Error("a malformed scope must not veto a valid sibling scope")
	}
}

// MatchSlot: web-slot paths, where one folder's mount physically holds its
// children's bytes (5/V). Distinct question from MatchGroups, one helper.
func TestMatchSlot(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		path    string
		want    bool
	}{
		{"own slot, file at root", []string{"atlas"}, "atlas/index.html", true},
		{"own slot, nested file", []string{"atlas"}, "atlas/reports/q3.html", true},
		{"own slot, bare folder", []string{"atlas"}, "atlas", true},

		// The bug this replaces: a multi-segment folder's own slot 403'd
		// because proxyd matched only the FIRST URL segment.
		{"deep folder reads its own slot", []string{"atlas/search"}, "atlas/search/page.html", true},
		{"deep folder, nested file", []string{"atlas/search"}, "atlas/search/a/b.html", true},
		{"deep folder cannot read the parent's file", []string{"atlas/search"}, "atlas/index.html", false},

		// Slot nesting, not grant inheritance: web/priv/atlas is the parent's
		// RW bind-mount and physically contains web/priv/atlas/search.
		{"parent slot contains child slot", []string{"atlas"}, "atlas/search/page.html", true},

		{"operator", []string{"**"}, "atlas/search/page.html", true},
		{"subtree glob", []string{"atlas/**"}, "atlas/search/page.html", true},
		{"wrong tenant", []string{"research"}, "atlas/file.html", false},
		{"tenant name is not a string prefix", []string{"atlas"}, "atlasx/file.html", false},
		{"no grants", nil, "atlas/file.html", false},
		{"empty path", []string{"atlas"}, "", false},
		{"slash-only path", []string{"atlas"}, "/", false},
		{"leading slash tolerated", []string{"atlas"}, "/atlas/file.html", true},
	}
	for _, c := range cases {
		if got := MatchSlot(c.allowed, c.path); got != c.want {
			t.Errorf("%s: MatchSlot(%v, %q) = %v, want %v",
				c.name, c.allowed, c.path, got, c.want)
		}
	}
}
