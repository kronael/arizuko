package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestFeature_MessageRouting(t *testing.T) {
	t.Run("prefix-precedence", func(t *testing.T) {
		routes := []core.Route{
			{Seq: 0, Match: "platform=slack", Target: "eng"},
			{Seq: 1, Match: "", Target: "inbox"},
		}
		got := router.ResolveRouteTarget(core.Message{ChatJID: "slack:T/C/U", Verb: "message"}, routes).Folder
		if got != "eng" {
			t.Fatalf("routed to %q, want eng", got)
		}
	})

	t.Run("room-match", func(t *testing.T) {
		routes := []core.Route{{Match: "room=42", Target: "sales"}}
		got := router.ResolveRouteTarget(core.Message{ChatJID: "telegram:42", Verb: "message"}, routes).Folder
		if got != "sales" {
			t.Fatalf("routed to %q, want sales", got)
		}
	})

	t.Run("no-match", func(t *testing.T) {
		routes := []core.Route{{Match: "platform=discord", Target: "gaming"}}
		got := router.ResolveRouteTarget(core.Message{ChatJID: "slack:T/C/U", Verb: "message"}, routes).Folder
		if got != "" {
			t.Fatalf("routed to %q, want no target", got)
		}
	})

	t.Run("sticky-pin", func(t *testing.T) {
		s := mustMonolithDB(t)
		if err := s.PutGroup(core.Group{Folder: "pinned"}); err != nil {
			t.Fatal(err)
		}
		if err := s.SetStickyGroup("telegram:99", "pinned"); err != nil {
			t.Fatal(err)
		}
		if got := s.GetStickyGroup("telegram:99"); got != "pinned" {
			t.Fatalf("sticky group = %q, want pinned", got)
		}
	})

	t.Run("inbound-to-dispatch", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.routdDB.AddRoute(core.Route{Match: "platform=slack", Target: "demo"}); err != nil {
			t.Fatal(err)
		}
		in := routdv1.Message{ID: "feat.route.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "hi", Verb: "message"}
		rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "slakd"), "", in)
		if rec.StatusCode != 200 {
			t.Fatalf("ingest status=%d", rec.StatusCode)
		}
		deadline := time.Now().Add(5 * time.Second)
		for f.dispatchedFolder("feat.route.1") == "" && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if got := f.dispatchedFolder("feat.route.1"); got != "demo" {
			t.Fatalf("dispatched to %q, want demo", got)
		}
		if got := countBotRows(t, f.routdDB, "slack:T/C/U"); got != 1 {
			t.Fatalf("bot rows = %d, want 1", got)
		}
	})
}
