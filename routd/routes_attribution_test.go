package routd

// Spec 5/18 step 7 stamps (added_by, added_via) on every routes row onbod writes.
// routd's writers stamp the same pair so ONE query over `routes` answers "who
// created this, and by which act" for every attributed writer — a reader must not
// have to know which daemon wrote a row to learn who wrote it, nor fall back to
// audit_log for half the table.

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// routeAttribution reads (added_by, added_via) for the route matching `match`.
// They are attributes of the item, so an unstamped rule carries neither and both
// read "" — the value that means "no actor recorded".
func routeAttribution(t *testing.T, db *DB, match string) (string, string) {
	t.Helper()
	for _, r := range routeRows(t, resreg.On(db.Obj())) {
		if r.Match == match {
			return r.AddedBy, r.AddedVia
		}
	}
	t.Fatalf("no route matching %q", match)
	return "", ""
}

// TestRoutesMCP_AddStampsAttribution: the agent socket's caller is the folder
// principal, and add_route records it plus the tool that ran.
func TestRoutesMCP_AddStampsAttribution(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", routeToolNames...)
	sock := serveRoutesMCP(t, db, "hq", "folder:hq")

	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"hq"}`,
	}); e != "" {
		t.Fatalf("add_route errored: %s", e)
	}

	by, via := routeAttribution(t, db, "platform=slack")
	if by != "folder:hq" {
		t.Errorf("added_by = %q, want the socket principal folder:hq", by)
	}
	if via != "add_route" {
		t.Errorf("added_via = %q, want add_route", via)
	}
}

// TestRoutesMCP_SetStampsEveryReplacedRow: set_routes REPLACES rows rather than
// editing them, so each survivor was created by this caller. Carrying the
// replaced rows' attribution forward would credit an act that did not happen.
func TestRoutesMCP_SetStampsEveryReplacedRow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", routeToolNames...)
	// An unstamped rule, written the way every live instance's rows were.
	if _, err := db.store().PutRouteRow(core.Route{Seq: 0, Match: "platform=old", Target: "hq"}); err != nil {
		t.Fatal(err)
	}
	sock := serveRoutesMCP(t, db, "hq", "folder:hq")

	if _, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":0,"match":"","target":"hq"},{"seq":1,"match":"platform=slack","target":"hq"}]`,
	}); e != "" {
		t.Fatalf("set_routes errored: %s", e)
	}

	rows := routeRows(t, resreg.On(db.Obj()))
	for _, r := range rows {
		if r.AddedBy != "folder:hq" || r.AddedVia != "set_routes" {
			t.Errorf("row %q attribution = (%q, %q), want (folder:hq, set_routes)",
				r.Match, r.AddedBy, r.AddedVia)
		}
	}
	if len(rows) != 2 {
		t.Errorf("set_routes left %d rows, want the 2 it wrote", len(rows))
	}
}

// TestRESTRouteStampsAttribution: the operator face records the JWT sub, not the
// folder principal — same columns, the caller its own surface proved.
func TestRESTRouteStampsAttribution(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u",
		scope: []string{"routes:write:own_group", "routes:read:own_group"}, folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "hq"})

	r := doJSON(t, h, "POST", "/v1/routes", "",
		map[string]any{"route": apiv1.Route{Seq: 1, Match: "platform=slack", Target: "hq"}})
	if r.Code != 200 {
		t.Fatalf("POST add = %d want 200 body=%s", r.Code, r.Body.String())
	}

	by, via := routeAttribution(t, db, "platform=slack")
	if by != "user:u" {
		t.Errorf("added_by = %q, want the REST caller user:u", by)
	}
	if via != "add_route" {
		t.Errorf("added_via = %q, want add_route", via)
	}
}

// TestUnattributedRoutesCarryNoActor: no backfill. A rule written by a caller
// that named neither field carries NEITHER attribute — absent is the only null
// this model has — and that must not be mistaken for an actor.
func TestUnattributedRoutesCarryNoActor(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.store().PutRouteRow(core.Route{Seq: 0, Match: "platform=legacy", Target: "hq"}); err != nil {
		t.Fatal(err)
	}
	items, err := db.Obj().Query(context.Background(), objstore.Query{
		Entity: store.Routes, Partition: store.OwnerRoutd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("the partition holds %d rules, want the one that was written", len(items))
	}
	_, by := items[0].Attrs["added_by"]
	_, via := items[0].Attrs["added_via"]
	if by || via {
		t.Errorf("an unattributed rule carries added_by=%v added_via=%v, want neither", by, via)
	}
}
