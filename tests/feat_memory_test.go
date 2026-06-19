// Memory / group-state feature tests: open flag, watchers, observe window,
// sticky topic, model override. These are the per-group knobs agents and
// dashd both read and write.
package tests

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestFeature_MemoryGroupState(t *testing.T) {
	// New group defaults open; SetGroupOpen(false) closes it.
	t.Run("group-open-flag", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "g"}); err != nil {
			t.Fatal(err)
		}
		if !s.IsGroupOpen("g") {
			t.Fatal("new group should default open")
		}
		if err := s.SetGroupOpen("g", false); err != nil {
			t.Fatal(err)
		}
		if s.IsGroupOpen("g") {
			t.Fatal("group should be closed after SetGroupOpen(false)")
		}
		if err := s.SetGroupOpen("g", true); err != nil {
			t.Fatal(err)
		}
		if !s.IsGroupOpen("g") {
			t.Fatal("group should be open after SetGroupOpen(true)")
		}
	})

	// Cross-folder ambient: add watcher → in WatchedSources; remove → gone.
	t.Run("group-watchers", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.AddGroupWatcher("corp/eng", "corp/sales"); err != nil {
			t.Fatal(err)
		}
		if got := s.WatchedSources("corp/eng"); len(got) != 1 || got[0] != "corp/sales" {
			t.Fatalf("watched = %v, want [corp/sales]", got)
		}
		if err := s.AddGroupWatcher("corp/eng", "corp/mktg"); err != nil {
			t.Fatal(err)
		}
		if got := s.WatchedSources("corp/eng"); len(got) != 2 {
			t.Fatalf("watched = %v, want 2 sources", got)
		}
		if err := s.RemoveGroupWatcher("corp/eng", "corp/sales"); err != nil {
			t.Fatal(err)
		}
		if got := s.WatchedSources("corp/eng"); len(got) != 1 || got[0] != "corp/mktg" {
			t.Fatalf("watched after remove = %v, want [corp/mktg]", got)
		}
	})

	// Observe window (msgs, chars) persists; partial updates preserve the omitted dimension.
	t.Run("observe-window-partial-update", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "ow"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGroupObserveWindow("ow", 10, 500); err != nil {
			t.Fatal(err)
		}
		if m, c := s.GroupObserveWindow("ow"); m != 10 || c != 500 {
			t.Fatalf("window = %d,%d want 10,500", m, c)
		}
		// Partial update: set only chars — msgs should be preserved.
		if err := s.SetGroupObserveWindow("ow", 0, 800); err != nil {
			t.Fatal(err)
		}
		if _, c := s.GroupObserveWindow("ow"); c != 800 {
			t.Fatalf("chars after partial update = %d, want 800", c)
		}
	})

	// Sticky group pin: chat JID bound to a folder overrides the route table.
	t.Run("sticky-group-round-trip", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "pinned"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetStickyGroup("telegram:99", "pinned"); err != nil {
			t.Fatal(err)
		}
		if got := s.GetStickyGroup("telegram:99"); got != "pinned" {
			t.Fatalf("sticky = %q, want pinned", got)
		}
		// Clearing the pin restores empty.
		if err := s.SetStickyGroup("telegram:99", ""); err != nil {
			t.Fatal(err)
		}
		if got := s.GetStickyGroup("telegram:99"); got != "" {
			t.Fatalf("sticky after clear = %q, want empty", got)
		}
	})

	// Per-group model override persists and reads back.
	t.Run("model-override-persists", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "m"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGroupModel("m", "claude-haiku-4-5"); err != nil {
			t.Fatal(err)
		}
		g, ok := s.GroupByFolder("m")
		if !ok || g.Model != "claude-haiku-4-5" {
			t.Fatalf("model = %q ok=%v, want claude-haiku-4-5", g.Model, ok)
		}
	})
}
