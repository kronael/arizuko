// dashd admin-plane feature tests: the routd REST surface that dashd writes
// through in the split topology. HTML page handlers live in dashd/*_test.go.
package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestFeature_DashdAdminPlane(t *testing.T) {
	// Route create persists and is visible in the route table.
	t.Run("routes-crud", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "world/sub"}); err != nil {
			t.Fatal(err)
		}
		tok := f.authd.mintService(t, "service:dashd", "routes:write")
		body := routdv1.Route{Seq: 10, Match: "room=42", Target: "world/sub"}
		rec := postBearer(t, f.routdTS.URL, "POST", "/v1/routes", tok, "", body)
		if rec.StatusCode != 200 && rec.StatusCode != 201 {
			t.Fatalf("route create = %d, want 2xx", rec.StatusCode)
		}
		routes, err := f.routdDB.Routes()
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, r := range routes {
			if r.Match == "room=42" && r.Target == "world/sub" {
				found = true
			}
		}
		if !found {
			t.Fatalf("route not persisted: %+v", routes)
		}
	})

	// Route create without routes:write → 403.
	t.Run("routes-crud-requires-scope", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "world/sub"}); err != nil {
			t.Fatal(err)
		}
		weak := f.authd.mintService(t, "service:rogue", "chats:read")
		body := routdv1.Route{Seq: 1, Match: "room=1", Target: "world/sub"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/routes", weak, "", body); rec.StatusCode != 403 {
			t.Fatalf("route without routes:write = %d, want 403", rec.StatusCode)
		}
	})

	// Grant add persists and is visible via ListACL.
	t.Run("grants-crud", func(t *testing.T) {
		f := bootFederation(t)
		tok := f.authd.mintService(t, "service:dashd", "acl:write")
		body := map[string]string{"principal": "alice@x", "scope": "world/sub", "action": "admin", "effect": "allow"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/acl", tok, "", body); rec.StatusCode != 200 {
			t.Fatalf("grant add = %d, want 200", rec.StatusCode)
		}
		if len(f.routdDB.ListACL("alice@x")) == 0 {
			t.Fatal("grant not persisted")
		}
	})

	// Grant add without acl:write → 403.
	t.Run("grants-crud-requires-scope", func(t *testing.T) {
		f := bootFederation(t)
		weak := f.authd.mintService(t, "service:rogue", "chats:read")
		body := map[string]string{"principal": "bob@x", "scope": "world/sub", "action": "admin", "effect": "allow"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/acl", weak, "", body); rec.StatusCode != 403 {
			t.Fatalf("grant without acl:write = %d, want 403", rec.StatusCode)
		}
	})

	// Task creation via the shared DB (write-discipline: dashd writes tasks
	// directly to the DB it mounts; this exercises the store contract dashd uses).
	t.Run("task-create-via-store", func(t *testing.T) {
		s, _ := openSharedDB(t)
		now := time.Now()
		next := now.Add(time.Hour)
		if err := s.CreateTask(core.Task{
			ID: "dash-t1", Owner: "main", ChatJID: "tg:1", Prompt: "daily brief",
			Cron: "0 8 * * *", NextRun: &next, Status: "active", Created: now,
		}); err != nil {
			t.Fatal(err)
		}
		got, ok := s.GetTask("dash-t1")
		if !ok || got.Owner != "main" || got.Cron != "0 8 * * *" {
			t.Fatalf("task = %+v ok=%v", got, ok)
		}
	})
}
