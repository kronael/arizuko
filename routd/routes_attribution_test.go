package routd

// Spec 5/18 step 7 stamps (added_by, added_via) on every routes row onbod writes.
// routd's writers stamp the same pair so ONE query over `routes` answers "who
// created this, and by which act" for every attributed writer — a reader must not
// have to know which daemon wrote a row to learn who wrote it, nor fall back to
// audit_log for half the table.

import (
	"testing"

	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// routeAttribution reads (added_by, added_via) for the route matching `match`.
// COALESCE, so a NULL column reads "" — the pre-attribution value.
func routeAttribution(t *testing.T, db *DB, match string) (string, string) {
	t.Helper()
	var by, via string
	if err := db.SQL().QueryRow(
		`SELECT COALESCE(added_by, ''), COALESCE(added_via, '') FROM routes WHERE match = ?`,
		match).Scan(&by, &via); err != nil {
		t.Fatalf("read attribution for %q: %v", match, err)
	}
	return by, via
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
	// A pre-attribution row, written the way every live instance's rows were.
	if _, err := db.SQL().Exec(
		`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=old', 'hq')`); err != nil {
		t.Fatal(err)
	}
	sock := serveRoutesMCP(t, db, "hq", "folder:hq")

	if _, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":0,"match":"","target":"hq"},{"seq":1,"match":"platform=slack","target":"hq"}]`,
	}); e != "" {
		t.Fatalf("set_routes errored: %s", e)
	}

	rows, err := db.SQL().Query(
		`SELECT match, COALESCE(added_by, ''), COALESCE(added_via, '') FROM routes`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var match, by, via string
		if err := rows.Scan(&match, &by, &via); err != nil {
			t.Fatal(err)
		}
		n++
		if by != "folder:hq" || via != "set_routes" {
			t.Errorf("row %q attribution = (%q, %q), want (folder:hq, set_routes)", match, by, via)
		}
	}
	if n != 2 {
		t.Errorf("set_routes left %d rows, want the 2 it wrote", n)
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

// TestPreAttributionRoutesStayNull: no backfill. A row written before the
// columns existed keeps NULL, which means "no actor recorded" and must not be
// mistaken for one.
func TestPreAttributionRoutesStayNull(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.SQL().Exec(
		`INSERT INTO routes (seq, match, target) VALUES (0, 'platform=legacy', 'hq')`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM routes WHERE added_by IS NULL AND added_via IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("pre-attribution row is not NULL/NULL; got %d such rows", n)
	}
}
