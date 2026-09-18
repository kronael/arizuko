package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// ListRoutes scopes on `target`, which carries a `#topic` fragment as well as
// a `/` subtree, so both separators go through the same predicate.
func TestListRoutesIsNotWidenedByLikeMetacharacters(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for _, target := range []string{"a_b", "a_b/one", "a_b#t", "aXb", "aXb/two", "aXb#t"} {
		if _, err := s.PutRouteRow(core.Route{Seq: 1, Match: "platform=slack", Target: target}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range s.ListRoutes("a_b", false) {
		if r.Target == "aXb" || r.Target == "aXb/two" || r.Target == "aXb#t" {
			t.Errorf("ListRoutes(a_b) returned %q", r.Target)
		}
	}
	if n := len(s.ListRoutes("a_b", false)); n != 3 {
		t.Errorf("ListRoutes(a_b) returned %d rows, want its own 3", n)
	}
}

// SiblingFolders scopes on the PARENT's subtree, then keeps only rows whose
// ParentOf is exactly that parent. The Go check is the containment boundary
// here — it is exact and case-sensitive, so it rejects what a widened SQL
// predicate would return. Mutating SQLSubtree back to LIKE therefore does NOT
// turn this test red, which is why it asserts the pair and not the predicate.
func TestSiblingFoldersKeepOnlyExactChildren(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for _, f := range []string{"a_b", "a_b/one", "a_b/two", "aXb", "aXb/three", "A_B", "A_B/four"} {
		if err := s.PutGroup(core.Group{Folder: f}); err != nil {
			t.Fatal(err)
		}
	}
	got := s.SiblingFolders("a_b/one")
	if len(got) != 1 || got[0] != "a_b/two" {
		t.Errorf("SiblingFolders(a_b/one) = %v, want [a_b/two] — not the parent, not aXb's child, not A_B's", got)
	}
}
