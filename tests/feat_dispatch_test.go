package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestFeature_AgentDispatch(t *testing.T) {
	t.Run("spawn-persisted", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.routdDB.AddRoute(core.Route{Match: "", Target: "demo"}); err != nil {
			t.Fatal(err)
		}
		in := routdv1.Message{ID: "feat.spawn.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "go", Verb: "message"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in); rec.StatusCode != 200 {
			t.Fatalf("ingest status=%d", rec.StatusCode)
		}
		testutils.WaitForRow(t, f.runedDB.SQL(), `SELECT COUNT(*) FROM spawns`, nil, 5*time.Second)
	})

	t.Run("model-override", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "corp/eng"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGroupModel("corp/eng", "claude-haiku"); err != nil {
			t.Fatal(err)
		}
		g, ok := s.GroupByFolder("corp/eng")
		if !ok || g.Model != "claude-haiku" {
			t.Fatalf("group model = %q (ok=%v), want claude-haiku", g.Model, ok)
		}
	})

	t.Run("context-mode", func(t *testing.T) {
		s, _ := openSharedDB(t)
		now := time.Now()
		if err := s.CreateTask(core.Task{
			ID: "ctx-1", Owner: "main", ChatJID: "tg:1", Prompt: "p",
			Status: "active", Created: now, ContextMode: "isolated",
		}); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetTask("ctx-1")
		if !ok || got.ContextMode != "isolated" {
			t.Fatalf("task context mode = %q (ok=%v), want isolated", got.ContextMode, ok)
		}
	})

	t.Run("observe-window", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "w"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetGroupObserveWindow("w", 5, 200); err != nil {
			t.Fatal(err)
		}
		if m, c := s.GroupObserveWindow("w"); m != 5 || c != 200 {
			t.Fatalf("window = %d,%d want 5,200", m, c)
		}
	})
}
