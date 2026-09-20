package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
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
	// core.GroupConfig cannot render the leftover key, so the fixture writes the
	// attribute groups.go declares for the spawn document.
	key, err := groupKey("rhias")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WriteEntities(context.Background(), nil, objstore.Write{
		Op: objstore.Update, Entity: Groups, Key: key,
		Attrs: map[string]string{
			attrGrpContainerConfig: `{"Mounts":null,"Timeout":90000000000,"MaxChildren":16}`,
		},
	}); err != nil {
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

// TestDeleteGroup_CascadesEveryPointerAtomically pins the ONE delete. The
// operator's `arizuko group rm` used to call a bare `DELETE FROM groups`
// (DeleteGroupRow) while routd's rollback called a cascading twin, so the only
// delete an operator could actually run left routes aimed at a dead folder and
// scheduled_tasks timed fired forever (BUGS L66/L67). marinade still carries 1
// orphan route and 4 orphan engagement rows from that path.
func TestDeleteGroup_CascadesEveryPointerAtomically(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.PutGroupRow(core.Group{Folder: "doomed", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroupRow(core.Group{Folder: "keeper", AddedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRouteRow(core.Route{Seq: 0, Match: "room=x", Target: "doomed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRouteRow(core.Route{Seq: 1, Match: "room=y", Target: "doomed#observe"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutRouteRow(core.Route{Seq: 2, Match: "room=z", Target: "keeper"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedDefaultTasks("doomed", "doomed"); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedDefaultTasks("keeper", "keeper"); err != nil {
		t.Fatal(err)
	}

	if _, _, err := s.DeleteGroup("doomed"); err != nil {
		t.Fatalf("DeleteGroup: %v", err)
	}

	if _, ok := s.GroupByFolder("doomed"); ok {
		t.Error("the groups row survived its own delete")
	}
	for _, c := range []struct {
		what string
		got  int
		want int
	}{
		{"routes aimed at doomed", routesTo(t, s, "doomed"), 0},
		{"doomed's tasks", tasksOwnedBy(t, s, "doomed"), 0},
	} {
		if c.got != c.want {
			t.Errorf("%s left %d row(s), want %d", c.what, c.got, c.want)
		}
	}
	// A sibling folder must not be touched: the purge keys on the exact
	// folder, and `doomed` is a prefix of nothing here by accident.
	if _, ok := s.GroupByFolder("keeper"); !ok {
		t.Error("keeper's group row went with doomed's")
	}
	if kept := routesTo(t, s, "keeper"); kept != 1 {
		t.Errorf("keeper's route count = %d, want 1", kept)
	}
	if keptTasks := tasksOwnedBy(t, s, "keeper"); keptTasks == 0 {
		t.Error("keeper lost its tasks: the purge is not scoped to the deleted folder")
	}
}

// The group row carries `open` and the observe windows so ONE read answers the
// settings page, instead of a dashboard reaching into this DB for them (spec
// 6/28). Their defaults must match the accessors they replace, or a group with
// no config key reads as closed and capped at nothing.
func TestGroupRowProjectsConfigDefaultsLikeTheAccessors(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}

	g, ok := s.GroupByFolder("acme")
	if !ok {
		t.Fatal("group not found")
	}
	// A group with no `open` key is OPEN — same as IsGroupOpen.
	if !g.Open || !s.IsGroupOpen("acme") {
		t.Errorf("Open = %v, IsGroupOpen = %v; a group with no key is open", g.Open, s.IsGroupOpen("acme"))
	}
	// An unset window is -1 (no cap), never 0 (a cap of nothing).
	wantM, wantC := s.GroupObserveWindow("acme")
	if g.ObserveWindowMessages != wantM || g.ObserveWindowChars != wantC {
		t.Errorf("row window = (%d,%d), accessor = (%d,%d)",
			g.ObserveWindowMessages, g.ObserveWindowChars, wantM, wantC)
	}
	if g.ObserveWindowMessages != -1 {
		t.Errorf("unset window = %d, want -1", g.ObserveWindowMessages)
	}
}

// Set values reach the row, and the accessors and the row keep agreeing.
func TestGroupRowTracksTheSetters(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupOpen("acme", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGroupObserveWindow("acme", 12, 3400); err != nil {
		t.Fatal(err)
	}

	g, _ := s.GroupByFolder("acme")
	if g.Open {
		t.Error("row still reports open after SetGroupOpen(false)")
	}
	if g.ObserveWindowMessages != 12 || g.ObserveWindowChars != 3400 {
		t.Errorf("row window = (%d,%d), want (12,3400)", g.ObserveWindowMessages, g.ObserveWindowChars)
	}

	// PutGroup writes container_config, never config — so a round-trip through
	// core.Group must not clobber what the setters wrote.
	if err := s.PutGroup(g); err != nil {
		t.Fatal(err)
	}
	again, _ := s.GroupByFolder("acme")
	if again.Open || again.ObserveWindowMessages != 12 {
		t.Errorf("a PutGroup round-trip clobbered config values: %+v", again)
	}
}

// routesTo counts the rules aimed at one folder, its topics included.
func routesTo(t *testing.T, s *Store, folder string) int {
	t.Helper()
	rows, err := s.RouteTable()
	if err != nil {
		t.Fatalf("RouteTable: %v", err)
	}
	n := 0
	for _, r := range rows {
		if r.Target == folder || strings.HasPrefix(r.Target, folder+"#") {
			n++
		}
	}
	return n
}

// tasksOwnedBy counts one folder's scheduled tasks, read as root so the count
// is the whole table's and not a scope's.
func tasksOwnedBy(t *testing.T, s *Store, folder string) int {
	t.Helper()
	n := 0
	for _, task := range s.ListTasks("", true) {
		if task.Owner == folder {
			n++
		}
	}
	return n
}
