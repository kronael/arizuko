package routd

// Parity tests for the spec 5/16 web_routes pilot: the agent's
// set_web_route/del_web_route/list_web_routes now ride resreg's two-face
// mechanism through the ServeMCP postBuild seam instead of hand-rolled ipc
// bodies. Each test drives the REAL unix socket end-to-end (not the handler
// directly) so the seam + injected Gate + Visible predicate are exercised.
//
// 5/33-paths-roles: none of the three tools is in the role:member floor, so every test that
// drives one delegates it with grantMCPTools (scope <folder>/**). The Gate binds the
// caller's OWN folder (the handler scopes every row by folder), so containment is
// the grant covering that folder.

import (
	"bufio"
	"encoding/json"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveWebRoutesMCP stands up the agent socket for folder + the web_routes resreg
// seam, and returns the socket path. Authz reads the folder's acl rows.
func serveWebRoutesMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.webRoutesPostBuild(folder, callerSub, srv.turnAuthorize(turnMCP{folder: folder}),
		srv.turnVisible(turnMCP{folder: folder}))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, false, 0, callerSub, pb)
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

// listToolNames returns the tool names advertised by tools/list on sock.
func listToolNames(t *testing.T, sock string) map[string]bool {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	req := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	// The turn's work crosses a network store before the response comes back, so
	// the deadline is the turn's budget and not a local socket's. Two seconds
	// timed out on a loaded host while the send had already succeeded.
	c.SetReadDeadline(time.Now().Add(mcpReadDeadline))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", resp, err)
	}
	names := map[string]bool{}
	for _, tl := range parsed.Result.Tools {
		names[tl.Name] = true
	}
	return names
}

// TestWebRoutesMCP_CreateListDelete: happy path for a delegated folder — set
// upserts into its own slot, list returns the row, delete removes it.
func TestWebRoutesMCP_CreateListDelete(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq")

	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/app", "access": "public",
	}); e != "" {
		t.Fatalf("set_web_route own-slot errored: %s", e)
	}
	rows, _ := db.WebRoutes("hq")
	if len(rows) != 1 || rows[0].PathPrefix != "/pub/hq/app" || rows[0].Access != "public" {
		t.Fatalf("route not persisted under folder: %+v", rows)
	}

	arr, e := callToolArray(t, sock, "list_web_routes", nil)
	if e != "" {
		t.Fatalf("list_web_routes errored: %s", e)
	}
	if len(arr) != 1 {
		t.Fatalf("list_web_routes returned %d rows, want 1: %v", len(arr), arr)
	}

	if _, e := callToolText(t, sock, "del_web_route", map[string]any{"path": "/pub/hq/app"}); e != "" {
		t.Fatalf("del_web_route own route errored: %s", e)
	}
	if rows, _ := db.WebRoutes("hq"); len(rows) != 0 {
		t.Fatalf("route still present after delete: %+v", rows)
	}
}

// TestWebRoutesMCP_SelfSlotAndPathClaim: create folds in the bespoke semantics —
// redirect must stay in the caller's own slot; a top-level prefix already owned
// by another folder can't be hijacked; the caller's own slot is always allowed.
func TestWebRoutesMCP_SelfSlotAndPathClaim(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "other"})
	// other already owns a top-level prefix.
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/shared", Access: "public", Folder: "other"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq")

	// redirect into another folder's slot: rejected.
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/r", "access": "redirect", "redirect_to": "/pub/other/x",
	}); e == "" {
		t.Fatal("cross-folder redirect should be rejected")
	}
	// hijacking a claimed top-level prefix: rejected.
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/shared", "access": "public",
	}); e == "" {
		t.Fatal("claim hijack should be rejected")
	}
	if owner, _ := db.WebRouteOwner("/shared"); owner != "other" {
		t.Fatalf("/shared owner changed to %q, want other", owner)
	}
	// own slot: allowed.
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/ok", "access": "public",
	}); e != "" {
		t.Fatalf("own-slot set errored: %s", e)
	}
}

// TestWebRoutesMCP_DeleteFolderBound: a caller delegated del_web_route may delete
// only its OWN routes — a cross-folder delete returns not-found. The route folder is
// the socket folder, never a client arg.
func TestWebRoutesMCP_DeleteFolderBound(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/eng"})
	_ = db.PutGroup(core.Group{Folder: "world/ops"})
	// Delegate world/eng the delete tool, scoped exactly to itself.
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:world/eng", Action: "mcp:del_web_route",
		Scope: "world/eng", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/pub/world/eng/x", Access: "public", Folder: "world/eng"})
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/pub/world/ops/y", Access: "public", Folder: "world/ops"})
	sock := serveWebRoutesMCP(t, db, "world/eng", "folder:world/eng")

	// cross-folder delete: bound to world/eng → miss.
	if _, e := callToolText(t, sock, "del_web_route", map[string]any{"path": "/pub/world/ops/y"}); e == "" {
		t.Fatal("cross-folder delete should fail (not owned by caller)")
	}
	if owner, ok := db.WebRouteOwner("/pub/world/ops/y"); !ok || owner != "world/ops" {
		t.Fatalf("world/ops route deleted cross-folder: owner=%q ok=%v", owner, ok)
	}
	// own delete: succeeds.
	if _, e := callToolText(t, sock, "del_web_route", map[string]any{"path": "/pub/world/eng/x"}); e != "" {
		t.Fatalf("own-folder delete errored: %s", e)
	}
	if _, ok := db.WebRouteOwner("/pub/world/eng/x"); ok {
		t.Fatal("own route not deleted")
	}
}

// TestWebRoutesMCP_DeleteTenantNoCrossTenant is the fail-on-broken guard for the
// 5/16 list-all leak class. A top-level tenant folder delegated del_web_route must
// still NOT delete a sibling tenant's route: the delete widening keys on the EMPTY
// folder claim, never on the caller being top-level. Before the fix a top-level
// caller widened the delete scope to "" and removed any folder's route (then
// set_web_route could re-claim it — cross-tenant DoS + hijack).
func TestWebRoutesMCP_DeleteTenantNoCrossTenant(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "globex"})
	putWebRoute(t, db, WebRouteRow{PathPrefix: "/pub/globex/landing", Access: "public", Folder: "globex"})
	// acme is a tenant granted del_web_route — so the delete is permitted by the grant
	// gate and only folder-containment can still block the cross-tenant hit.
	grantMCPTools(t, db, "acme", "del_web_route")
	sock := serveWebRoutesMCP(t, db, "acme", "folder:acme")

	if _, e := callToolText(t, sock, "del_web_route", map[string]any{"path": "/pub/globex/landing"}); e == "" {
		t.Fatal("cross-tenant delete should fail (not owned by acme)")
	}
	if owner, ok := db.WebRouteOwner("/pub/globex/landing"); !ok || owner != "globex" {
		t.Fatalf("globex route deleted by tenant acme: owner=%q ok=%v", owner, ok)
	}
}

// TestWebRoutesMCP_Visibility: the Visible predicate (auth.EffectiveActions)
// preserves tools/list gating — a delegated folder sees the three tools; an
// ungranted folder sees none (no web_route tool is in the role:member floor).
func TestWebRoutesMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")

	granted := listToolNames(t, serveWebRoutesMCP(t, db, "hq", "folder:hq"))
	for _, name := range []string{"set_web_route", "del_web_route", "list_web_routes"} {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
	}
	ungranted := listToolNames(t, serveWebRoutesMCP(t, db, "world/a", "folder:world/a"))
	for _, name := range []string{"set_web_route", "del_web_route", "list_web_routes"} {
		if ungranted[name] {
			t.Fatalf("%s visible to a folder not granted it", name)
		}
	}
}

// TestWebRoutesMCP_GateDenies: a tool that is VISIBLE (an allow row grants it) but
// overridden by a DENY row is rejected at call time by the injected Gate
// (auth.Authorize is deny-wins), not by resreg's operator default.
func TestWebRoutesMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "set_web_route")
	// A deny row on the same (tool, scope) wins over the allow — visible, uncallable.
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:hq", Action: "mcp:set_web_route", Scope: "hq/**", Effect: "deny",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq")

	if !listToolNames(t, sock)["set_web_route"] {
		t.Fatal("set_web_route should be visible (an allow row grants it)")
	}
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/x", "access": "public",
	}); e == "" {
		t.Fatal("set_web_route should be denied by the deny row")
	}
	if rows, _ := db.WebRoutes("hq"); len(rows) != 0 {
		t.Fatalf("denied set still wrote a row: %+v", rows)
	}
}

// TestWebRoutesMCP_AuditRowLands: an agent mutation writes one audit_log row in
// routd.db via resreg's tx-bound EmitInTx (was emitSys/LogIPCAudit). Confirms
// the audit ROW still lands; note the shape change (action web_routes:create,
// resource web_routes, surface mcp) in the migration report.
func TestWebRoutesMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq")

	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/app", "access": "public",
	}); e != "" {
		t.Fatalf("set_web_route errored: %s", e)
	}
	n := 0
	for _, r := range auditRowsFor(t, db, "web_routes:create") {
		if r.Outcome == "ok" && r.Folder == "hq" && r.Surface == "mcp" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the audit trail holds %d web_routes:create rows, want 1", n)
	}
}

// TestWebRoutesMCP_RedirectShadowsAnotherSlot is the agent half of the L314
// guard: set_web_route is delegable, so one call from one folder could take the
// publish path of every other folder on the instance. The refusal names them.
func TestWebRoutesMCP_RedirectShadowsAnotherSlot(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"atlas", "coach", "eval"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "atlas", "set_web_route")
	sock := serveWebRoutesMCP(t, db, "atlas", "folder:atlas")

	_, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/", "access": "redirect", "redirect_to": "/pub/atlas/",
	})
	if e == "" {
		t.Fatal("a /pub/ redirect was accepted; it answers for every folder's publish path")
	}
	for _, want := range []string{"coach", "eval"} {
		if !strings.Contains(e, want) {
			t.Errorf("refusal does not name shadowed folder %q: %s", want, e)
		}
	}
	if _, ok := db.WebRouteOwner("/pub/"); ok {
		t.Fatal("the refused /pub/ redirect landed a row anyway")
	}
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/agents/", "access": "redirect", "redirect_to": "/pub/atlas/agents/",
	}); e != "" {
		t.Fatalf("narrow redirect that shadows no group was refused: %s", e)
	}
	if owner, _ := db.WebRouteOwner("/pub/agents/"); owner != "atlas" {
		t.Fatalf("/pub/agents/ owner = %q, want atlas", owner)
	}
}
