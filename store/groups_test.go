package store

import (
	"reflect"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestRouteSourceJIDs_RoomOnly(t *testing.T) {
	got := routeSourceJIDs("room=123")
	want := []string{"123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routeSourceJIDs(%q) = %v, want %v", "room=123", got, want)
	}
}

func TestRouteSourceJIDs_PlatformAndRoom(t *testing.T) {
	got := routeSourceJIDs("platform=telegram room=123")
	want := []string{"telegram:123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRouteSourceJIDs_ChatJID(t *testing.T) {
	got := routeSourceJIDs("chat_jid=telegram:123")
	want := []string{"telegram:123"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestRouteSourceJIDs_GlobSkipped(t *testing.T) {
	got := routeSourceJIDs("platform=telegram room=*")
	if len(got) != 0 {
		t.Fatalf("glob room should be skipped, got %v", got)
	}
}

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

// SetGroupMaxChildren uses a single json_set write into container_config so it
// can't lose a concurrent SetGroupModel/PutGroup update (no read-modify-write).
// This pins both halves: the value round-trips through GroupByFolder, and a
// prior SetGroupModel on the same row survives the json_set (one column, two
// independent fields).
func TestSetGroupMaxChildren_JSONSetPreservesOtherFields(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "main")

	if err := s.SetGroupModel("main", "claude-opus-4-8"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupMaxChildren("main", 7); err != nil {
		t.Fatal(err)
	}

	g, ok := s.GroupByFolder("main")
	if !ok {
		t.Fatal("group not found")
	}
	if g.Config.MaxChildren != 7 {
		t.Errorf("MaxChildren = %d, want 7 (json_set round-trip)", g.Config.MaxChildren)
	}
	if g.Model != "claude-opus-4-8" {
		t.Errorf("Model = %q, want claude-opus-4-8 (json_set must not clobber the model column)", g.Model)
	}

	// Overwrite: a second json_set updates the same key, no duplication.
	if err := s.SetGroupMaxChildren("main", 3); err != nil {
		t.Fatal(err)
	}
	g2, _ := s.GroupByFolder("main")
	if g2.Config.MaxChildren != 3 {
		t.Errorf("MaxChildren after re-set = %d, want 3", g2.Config.MaxChildren)
	}
}

// On a row with no container_config (NULL), SetGroupMaxChildren must still
// land the value — the COALESCE(container_config,'{}') seeds the JSON doc.
func TestSetGroupMaxChildren_FromNullConfig(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	putBareGroup(t, s, "solo")

	if err := s.SetGroupMaxChildren("solo", 4); err != nil {
		t.Fatal(err)
	}
	g, ok := s.GroupByFolder("solo")
	if !ok {
		t.Fatal("group not found")
	}
	if g.Config.MaxChildren != 4 {
		t.Errorf("MaxChildren from NULL config = %d, want 4", g.Config.MaxChildren)
	}
}
