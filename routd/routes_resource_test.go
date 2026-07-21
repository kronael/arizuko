package routd

// Parity tests for the spec 5/16 routes migration: the agent's
// add_route/set_routes/list_routes/delete_route now ride resreg's MCP mechanism
// through the ServeMCP postBuild seam instead of four hand-rolled ipc bodies. Each
// test drives the REAL unix socket end-to-end (not the handler directly) so the
// seam + injected Gate (grants.CheckAction + db.Authorize) + the handler's two
// security invariants + the Visible predicate are all exercised.
//
// Routing is security-critical (a route's TARGET folder decides which group a
// chat's turns fire in), so two invariants MUST hold and two tests fail if they're
// dropped:
//
//   - TARGET CONTAINMENT. TestRoutesMCP_SetRoutesCrossFolderDenied (tier-0, the
//     routeTargetWithin per-route check) and TestRoutesMCP_AddCrossFolderDenied /
//     TestRoutesMCP_DeleteCrossFolderDenied (tier-1, auth.AuthorizeStructural on
//     the arg target / the id-resolved target) fail if the containment is removed —
//     a folder would write/delete a route pointing at another folder.
//   - SELF-DEFAULT GUARD. TestRoutesMCP_SetRoutesSelfDefaultGuard and
//     TestRoutesMCP_DeleteSelfDefaultRefused fail if the guard is removed — a folder
//     would orphan its own seq-0 default route.
//
// Tier note: the route tools are in grants.tier1FixedActions — granted by default
// at tier 0 AND 1. set_routes' tier cap binds the OWN folder, so in practice only
// tier 0 can set_routes (a tier-1 caller fails HasPrefix(folder, folder+"/")); the
// cross-folder containment for add/delete is exercised at tier 1 where the cap
// actually confines the target.

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveRoutesMCP stands up the agent socket for folder with the given grant rules
// + the routes resreg seam, and returns the socket path.
func serveRoutesMCP(t *testing.T, db *DB, folder, callerSub string, rules []string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.routesPostBuild(folder, callerSub, rules, srv.db.Authorize, auth.Resolve(folder))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, rules, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// serveRoutesMCPElevated stands up the agent socket as an operator /root turn
// would: the tier-0 `*` grant set, an allow-all row-ACL (turnAuthorize(true))
// and a tier-0 EFFECTIVE identity (turnIdentity(folder, true)) — the exact
// wiring ServeTurnMCP hands the postBuild for an elevated turn.
func serveRoutesMCPElevated(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	rules := []string{"*"}
	callerSub := "folder:" + folder
	pb := srv.routesPostBuild(folder, callerSub, rules, srv.turnAuthorize(true), turnIdentity(folder, true))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, rules, 0, callerSub, pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for !fileExists(sock) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	return sock
}

// findRouteID returns the id of the (first) route with the given target, failing
// the test if none exists.
func findRouteID(t *testing.T, db *DB, target string) int64 {
	t.Helper()
	rows, err := db.Routes()
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	for _, r := range rows {
		if r.Target == target {
			return r.ID
		}
	}
	t.Fatalf("no route with target %q found", target)
	return 0
}

// TestRoutesMCP_AddListDeleteOwn: happy path for a tier-0 folder — add appends a
// rule, list returns it, delete removes it by id.
func TestRoutesMCP_AddListDeleteOwn(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	res, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"hq"}`,
	})
	if e != "" {
		t.Fatalf("add_route errored: %s", e)
	}
	rid, ok := res["id"].(float64)
	if !ok || int64(rid) == 0 {
		t.Fatalf("add_route returned no id: %v", res)
	}
	if got, _ := db.Routes(); len(got) != 1 || got[0].Target != "hq" {
		t.Fatalf("route not persisted: %+v", got)
	}

	out, e := callToolOverSock(t, sock, "list_routes", nil)
	if e != "" {
		t.Fatalf("list_routes errored: %s", e)
	}
	if routes, _ := out["routes"].([]any); len(routes) != 1 {
		t.Fatalf("list_routes = %v, want 1 route", out["routes"])
	}

	if _, e := callToolOverSock(t, sock, "delete_route", map[string]any{"id": rid}); e != "" {
		t.Fatalf("delete_route errored: %s", e)
	}
	if got, _ := db.Routes(); len(got) != 0 {
		t.Fatalf("route still present after delete: %+v", got)
	}
}

// TestRoutesMCP_SetRoutesBulkReplace: set_routes overwrites the folder's routes
// wholesale; the reply carries the new count and the table holds exactly the new
// set (all targeting the own folder).
func TestRoutesMCP_SetRoutesBulkReplace(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{
		{Seq: 0, Match: "room=old", Target: "hq"},
		{Seq: 1, Match: "platform=slack", Target: "hq"},
	})
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	res, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":0,"match":"room=new","target":"hq"},{"seq":2,"match":"platform=telegram","target":"hq"}]`,
	})
	if e != "" {
		t.Fatalf("set_routes errored: %s", e)
	}
	if res["count"].(float64) != 2 {
		t.Fatalf("count = %v, want 2", res["count"])
	}
	rows, _ := db.Routes()
	if len(rows) != 2 {
		t.Fatalf("routes after set = %d, want 2: %+v", len(rows), rows)
	}
	for _, r := range rows {
		if r.Target != "hq" {
			t.Fatalf("unexpected target after set: %+v", r)
		}
	}
}

// TestRoutesMCP_SetRoutesCrossFolderDenied: THE tier-0 containment guard. A route
// in a set_routes batch that targets ANOTHER folder is rejected by
// routeTargetWithin, and NOTHING is written (the pre-seeded table is intact). Fails
// if the per-route containment loop is removed — a cross-folder route would land.
func TestRoutesMCP_SetRoutesCrossFolderDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Seq: 0, Match: "room=hq", Target: "hq"}})
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":0,"match":"room=hq","target":"hq"},{"seq":1,"match":"platform=slack","target":"other"}]`,
	}); e == "" {
		t.Fatal("set_routes with a cross-folder target must be denied")
	}
	rows, _ := db.Routes()
	for _, r := range rows {
		if r.Target == "other" {
			t.Fatalf("denied set_routes still wrote a cross-folder route: %+v", r)
		}
	}
	if len(rows) != 1 || rows[0].Target != "hq" || rows[0].Match != "room=hq" {
		t.Fatalf("denied set_routes mutated the table: %+v", rows)
	}
}

// TestRoutesMCP_AddCrossFolderDenied: THE tier-1 containment guard on add. A tier-1
// caller (granted the tool by default) may route to a strict descendant, but NOT to
// a sibling or its parent — auth.AuthorizeStructural on the arg-carried target.
// Nothing writes on denial. Fails if the handler's AuthorizeStructural is dropped.
func TestRoutesMCP_AddCrossFolderDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/a/x", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	sock := serveRoutesMCP(t, db, "world/a", "folder:world/a", deriveFolderGrants(db, "world/a"))

	// descendant target: allowed.
	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"world/a/x"}`,
	}); e != "" {
		t.Fatalf("add_route to a descendant should be allowed: %s", e)
	}
	_ = findRouteID(t, db, "world/a/x") // must be written

	// sibling target: denied.
	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":2,"match":"platform=slack","target":"world/b"}`,
	}); e == "" {
		t.Fatal("add_route to a sibling folder must be denied")
	}
	// parent target: denied.
	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":3,"match":"platform=slack","target":"world"}`,
	}); e == "" {
		t.Fatal("add_route to the parent folder must be denied")
	}
	rows, _ := db.Routes()
	for _, r := range rows {
		if r.Target == "world/b" || r.Target == "world" {
			t.Fatalf("denied add_route still wrote a cross-folder route: %+v", r)
		}
	}
}

// TestRoutesMCP_RootElevationAddsCrossFolder is the elevated counterpart to
// TestRoutesMCP_AddCrossFolderDenied: a tier-1 folder cannot add_route to a
// sibling over the plain agent socket, but an operator /root turn from that
// SAME folder can — the structural gate sees tier 0 under elevation
// (turnIdentity), not the folder's static tier. Regression guard for the class
// of bug d452d6ef fixed.
func TestRoutesMCP_RootElevationAddsCrossFolder(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	sock := serveRoutesMCPElevated(t, db, "world/a")

	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"world/b"}`,
	}); e != "" {
		t.Fatalf("/root add_route to a sibling folder should be allowed: %s", e)
	}
	_ = findRouteID(t, db, "world/b") // must be written
}

// TestRoutesMCP_DeleteCrossFolderDenied: THE tier-1 containment guard on delete —
// the id-resolution case. delete_route resolves the route's TARGET from the id
// (s.db.GetRoute) before the tier cap rules on it, so a tier-1 caller may delete a
// route pointing at a descendant but NOT one pointing at a sibling. Fails if the
// handler's AuthorizeStructural on the resolved target is dropped (the sibling's
// route would be deleted).
func TestRoutesMCP_DeleteCrossFolderDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/a/x", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	doSetRoutes(t, db, []core.Route{
		{Seq: 1, Match: "platform=slack", Target: "world/a/x"},
		{Seq: 1, Match: "platform=telegram", Target: "world/b"},
	})
	ridDesc := findRouteID(t, db, "world/a/x")
	ridSib := findRouteID(t, db, "world/b")
	sock := serveRoutesMCP(t, db, "world/a", "folder:world/a", deriveFolderGrants(db, "world/a"))

	// sibling's route: id resolves to target world/b → denied, still present.
	if _, e := callToolOverSock(t, sock, "delete_route", map[string]any{"id": float64(ridSib)}); e == "" {
		t.Fatal("delete of a sibling folder's route must be denied")
	}
	if _, err := db.GetRoute(ridSib); err != nil {
		t.Fatal("denied cross-folder delete still removed the sibling route")
	}
	// descendant's route: allowed.
	if _, e := callToolOverSock(t, sock, "delete_route", map[string]any{"id": float64(ridDesc)}); e != "" {
		t.Fatalf("delete of a descendant route should be allowed: %s", e)
	}
	if _, err := db.GetRoute(ridDesc); err == nil {
		t.Fatal("descendant route not deleted")
	}
}

// TestRoutesMCP_SetRoutesSelfDefaultGuard: THE self-default guard on set. A
// replacement that drops the folder's seq-0 default route is refused (the default
// stays); one that keeps a seq-0 self-default is allowed. Fails if the guard is
// removed (the first set would wipe the default).
func TestRoutesMCP_SetRoutesSelfDefaultGuard(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Seq: 0, Match: "room=hq", Target: "hq"}})
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	// Replacement WITHOUT a seq-0 self-default → refused; the default stays.
	if _, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":5,"match":"platform=slack","target":"hq"}]`,
	}); e == "" {
		t.Fatal("set_routes that drops the seq-0 default must be refused")
	}
	rows, _ := db.Routes()
	if len(rows) != 1 || rows[0].Seq != 0 || rows[0].Match != "room=hq" {
		t.Fatalf("refused set_routes still mutated the default route: %+v", rows)
	}

	// Replacement that KEEPS a seq-0 self-default → allowed.
	if _, e := callToolOverSock(t, sock, "set_routes", map[string]any{
		"routes": `[{"seq":0,"match":"room=new","target":"hq"},{"seq":5,"match":"platform=slack","target":"hq"}]`,
	}); e != "" {
		t.Fatalf("set_routes keeping the default should be allowed: %s", e)
	}
	if rows, _ := db.Routes(); len(rows) != 2 {
		t.Fatalf("routes after keep-default set = %d, want 2", len(rows))
	}
}

// TestRoutesMCP_DeleteSelfDefaultRefused: the delete side of the self-default guard
// — a folder can't delete its own seq-0 default route.
func TestRoutesMCP_DeleteSelfDefaultRefused(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	doSetRoutes(t, db, []core.Route{{Seq: 0, Match: "room=hq", Target: "hq"}})
	rid := findRouteID(t, db, "hq")
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolOverSock(t, sock, "delete_route", map[string]any{"id": float64(rid)}); e == "" {
		t.Fatal("delete of the own seq-0 default route must be refused")
	}
	if _, err := db.GetRoute(rid); err != nil {
		t.Fatal("refused delete still removed the default route")
	}
}

// TestRoutesMCP_GateDenies: a tool that is VISIBLE (a wildcard rule matches) but
// DENIED by a later deny rule is rejected at call time by the injected Gate's
// grants.CheckAction layer, before the mutation runs.
func TestRoutesMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	// callerSub="" isolates the CheckAction layer (skips db.Authorize).
	sock := serveRoutesMCP(t, db, "hq", "", []string{"*", "!add_route"})

	if !listToolNames(t, sock)["add_route"] {
		t.Fatal("add_route should be visible (a wildcard rule matches it)")
	}
	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"hq"}`,
	}); e == "" {
		t.Fatal("add_route should be denied by the gate")
	}
	if rows, _ := db.Routes(); len(rows) != 0 {
		t.Fatalf("denied add_route still wrote a route: %+v", rows)
	}
}

// TestRoutesMCP_Visibility: the Visible predicate (MatchingRules) preserves
// tools/list gating — a tier-0 folder sees the four tools; a tier-2 folder (whose
// derived rules omit them) does not.
func TestRoutesMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a/b"})

	tier0 := listToolNames(t, serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq")))
	for _, name := range []string{"add_route", "set_routes", "list_routes", "delete_route"} {
		if !tier0[name] {
			t.Fatalf("%s not visible to a tier-0 folder", name)
		}
	}
	tier2 := listToolNames(t, serveRoutesMCP(t, db, "world/a/b", "folder:world/a/b", deriveFolderGrants(db, "world/a/b")))
	if tier2["add_route"] {
		t.Fatal("add_route visible to a tier-2 folder that isn't granted it")
	}
}

// TestRoutesMCP_AuditRowLands: an agent mutation writes one audit_log row in
// routd.db via resreg's tx-bound EmitInTx (was emitSys). The action is routes:add,
// resource routes, surface mcp.
func TestRoutesMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	sock := serveRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolOverSock(t, sock, "add_route", map[string]any{
		"route": `{"seq":1,"match":"platform=slack","target":"hq"}`,
	}); e != "" {
		t.Fatalf("add_route errored: %s", e)
	}
	var n int
	if err := db.SQL().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action='routes:add' AND outcome='ok' AND folder='hq' AND surface='mcp'`,
	).Scan(&n); err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log has %d routes:add rows, want 1", n)
	}
}

// TestIsSelfDefault + TestRouteTargetWithin: unit tests for the two containment
// helpers, moved here from ipc when the route tools migrated to resreg (their only
// callers moved with them).
func TestIsSelfDefault(t *testing.T) {
	cases := []struct {
		seq    int
		target string
		owner  string
		want   bool
	}{
		{0, "world/a", "world/a", true},
		{0, "folder:world/a", "world/a", true},
		{1, "world/a", "world/a", false},
		{0, "world/a/child", "world/a", false},
		{0, "world/b", "world/a", false},
	}
	for _, c := range cases {
		got := isSelfDefault(core.Route{Seq: c.seq, Target: c.target}, c.owner)
		if got != c.want {
			t.Errorf("isSelfDefault({Seq:%d,Target:%q},%q) = %v, want %v",
				c.seq, c.target, c.owner, got, c.want)
		}
	}
}

func TestRouteTargetWithin(t *testing.T) {
	cases := []struct {
		target, owner string
		want          bool
	}{
		{"world/a", "world/a", true},
		{"world/a/child", "world/a", true},
		{"world/a/deep/nested", "world/a", true},
		{"folder:world/a", "world/a", true},
		{"folder:world/a/child", "world/a", true},
		{"folder:world/b", "world/a", false},
		{"world/b", "world/a", false},
		{"world/ab", "world/a", false}, // must not prefix-match world/a in world/ab
		{"daemon:timed", "world/a", false},
		{"builtin:stop", "world/a", false},
	}
	for _, c := range cases {
		if got := routeTargetWithin(c.target, c.owner); got != c.want {
			t.Errorf("routeTargetWithin(%q, %q) = %v, want %v", c.target, c.owner, got, c.want)
		}
	}
}
