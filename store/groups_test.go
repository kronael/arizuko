package store

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// putBareGroup is a test helper: minimal PutGroup that lands a row with
// the schema defaults applied (open=1, observe-window NULLs).
func putBareGroup(t *testing.T, s *Store, folder string) {
	t.Helper()
	if err := s.PutGroup(core.Group{Folder: folder, AddedAt: time.Now()}); err != nil {
		t.Fatalf("PutGroup(%q): %v", folder, err)
	}
}

func TestIsGroupOpen_DefaultTrueOnMissing(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if !s.IsGroupOpen("nope/missing") {
		t.Fatal("missing row should default to open=true")
	}
}

func TestSetGroupOpen_Flip(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "main/a")
	if !s.IsGroupOpen("main/a") {
		t.Fatal("fresh row should be open")
	}
	if err := s.SetGroupOpen("main/a", false); err != nil {
		t.Fatal(err)
	}
	if s.IsGroupOpen("main/a") {
		t.Fatal("after SetGroupOpen(false) should be closed")
	}
	if err := s.SetGroupOpen("main/a", true); err != nil {
		t.Fatal(err)
	}
	if !s.IsGroupOpen("main/a") {
		t.Fatal("after SetGroupOpen(true) should be open")
	}
}

func TestGroupObserveWindow_NULLBehavior(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "main/a")
	m, c := s.GroupObserveWindow("main/a")
	if m != -1 || c != -1 {
		t.Fatalf("NULL caps = (%d,%d), want (-1,-1)", m, c)
	}
	if err := s.SetGroupObserveWindow("main/a", 25, 8000); err != nil {
		t.Fatal(err)
	}
	m, c = s.GroupObserveWindow("main/a")
	if m != 25 || c != 8000 {
		t.Fatalf("after set = (%d,%d), want (25,8000)", m, c)
	}
}

func TestSetGroupObserveWindow_ClearViaNegOne(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "main/a")
	if err := s.SetGroupObserveWindow("main/a", 25, 8000); err != nil {
		t.Fatal(err)
	}
	// Clear messages, keep chars set.
	if err := s.SetGroupObserveWindow("main/a", -1, 8000); err != nil {
		t.Fatal(err)
	}
	m, c := s.GroupObserveWindow("main/a")
	if m != -1 || c != 8000 {
		t.Fatalf("partial clear = (%d,%d), want (-1,8000)", m, c)
	}
	// Clear both.
	if err := s.SetGroupObserveWindow("main/a", -1, -1); err != nil {
		t.Fatal(err)
	}
	m, c = s.GroupObserveWindow("main/a")
	if m != -1 || c != -1 {
		t.Fatalf("full clear = (%d,%d), want (-1,-1)", m, c)
	}
}

func TestSiblingFolders_FiltersClosedAndSelf(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, f := range []string{"main/a", "main/b", "main/c", "main/c/deep", "other/x"} {
		putBareGroup(t, s, f)
	}
	if err := s.SetGroupOpen("main/c", false); err != nil {
		t.Fatal(err)
	}
	got := s.SiblingFolders("main/a")
	want := map[string]bool{"main/b": true}
	if len(got) != len(want) {
		t.Fatalf("siblings = %v, want %v", got, want)
	}
	for _, f := range got {
		if !want[f] {
			t.Errorf("unexpected sibling %q", f)
		}
	}
}

func TestSiblingFolders_RootHasNone(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "main")
	putBareGroup(t, s, "other")
	if got := s.SiblingFolders("main"); len(got) != 0 {
		t.Errorf("root sibling list = %v, want []", got)
	}
}

// A leftover MaxChildren key in container_config (written before the prototype
// spawn was removed) must decode harmlessly: core.GroupConfig no longer has the
// field, and the unknown key must neither error nor disturb Mounts/Timeout. This
// is why the removal shipped without a data migration.
func TestGroupByFolder_IgnoresLeftoverMaxChildren(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "rhias")
	if _, err := s.DB().Exec(
		`UPDATE groups SET container_config = ? WHERE folder = ?`,
		`{"Mounts":null,"Timeout":90000000000,"MaxChildren":16}`, "rhias"); err != nil {
		t.Fatal(err)
	}

	g, ok := s.GroupByFolder("rhias")
	if !ok {
		t.Fatal("group not found")
	}
	if g.Config.Timeout != 90*time.Second {
		t.Errorf("Timeout = %v, want 90s — a leftover MaxChildren key must not disturb decoding", g.Config.Timeout)
	}
}
