package routd

// Parity tests for the spec 5/44 web_routes pilot: the agent's
// set_web_route/del_web_route/list_web_routes now ride resreg's two-face
// mechanism through the ServeMCP postBuild seam instead of hand-rolled ipc
// bodies. Each test drives the REAL unix socket end-to-end (not the handler
// directly) so the seam + injected tier Gate + Visible predicate are exercised.
//
// Tier note: these three tools are granted only at tier 0 (DeriveRules tier-0 =
// ["*"]); tiers 1+ don't get them by default. So the happy-path tests use a
// tier-0 (top-level) folder and pass its REAL derived rules, keeping the gate's
// two layers (grants.CheckAction over the rules + db.Authorize re-deriving the
// same tier defaults) consistent — exactly as production ServeTurnMCP wires it.

import (
	"bufio"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveWebRoutesMCP stands up the agent socket for folder with the given grant
// rules + the web_routes resreg seam, and returns the socket path.
func serveWebRoutesMCP(t *testing.T, db *DB, folder, callerSub string, rules []string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.webRoutesPostBuild(folder, callerSub, rules)
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
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
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

// TestWebRoutesMCP_CreateListDelete: happy path for a tier-0 folder — set
// upserts into its own slot, list returns the row, delete removes it (the
// tier-0 widening reaches the row).
func TestWebRoutesMCP_CreateListDelete(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")
	rules := deriveFolderGrants(db, "hq")
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq", rules)

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
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/shared", Access: "public", Folder: "other"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")
	rules := deriveFolderGrants(db, "hq")
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq", rules)

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

// TestWebRoutesMCP_DeleteFolderBound: a NON-tier-0 caller (granted del_web_route
// via an ACL overlay) may delete only its OWN routes — a cross-folder delete
// returns not-found. This is the folder-authz the tier gate + handler preserve;
// the route folder is the socket folder, never a client arg.
func TestWebRoutesMCP_DeleteFolderBound(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/eng"})
	_ = db.PutGroup(core.Group{Folder: "world/ops"})
	// grant tier-1 world/eng the delete tool (tiers 1+ lack it by default).
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:world/eng", Action: "mcp:del_web_route",
		Scope: "world/eng", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/world/eng/x", Access: "public", Folder: "world/eng"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/world/ops/y", Access: "public", Folder: "world/ops"})
	rules := deriveFolderGrants(db, "world/eng")
	sock := serveWebRoutesMCP(t, db, "world/eng", "folder:world/eng", rules)

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

// TestWebRoutesMCP_DeleteTier0TenantNoCrossTenant is the fail-on-broken guard for
// the 5/44 list-all leak class. A top-level tenant folder is tier-0
// (min(count("/"),3)==0) and holds ["*"] grants by default, so del_web_route is
// GRANTED — yet it must NOT delete a sibling tenant's route. Containment keys on
// the EMPTY folder claim, never tier-0. Before the fix, tier-0 widened the delete
// scope to "" and removed any folder's route (then set_web_route could re-claim
// it — cross-tenant DoS + hijack).
func TestWebRoutesMCP_DeleteTier0TenantNoCrossTenant(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "acme"})
	_ = db.PutGroup(core.Group{Folder: "globex"})
	_ = db.PutWebRoute(WebRouteRow{PathPrefix: "/pub/globex/landing", Access: "public", Folder: "globex"})
	// acme is a tier-1 tenant granted del_web_route — so the delete is permitted by
	// the grant gate and only folder-containment can still block the cross-tenant hit.
	grantMCPTools(t, db, "acme", "del_web_route")
	rules := deriveFolderGrants(db, "acme")
	sock := serveWebRoutesMCP(t, db, "acme", "folder:acme", rules)

	if _, e := callToolText(t, sock, "del_web_route", map[string]any{"path": "/pub/globex/landing"}); e == "" {
		t.Fatal("tier-0 tenant cross-tenant delete should fail (not owned by acme)")
	}
	if owner, ok := db.WebRouteOwner("/pub/globex/landing"); !ok || owner != "globex" {
		t.Fatalf("globex route deleted by tier-0 tenant acme: owner=%q ok=%v", owner, ok)
	}
}

// TestWebRoutesMCP_Visibility: the Visible predicate (MatchingRules) preserves
// tools/list gating — a tier-0 folder (rules ["*"]) sees set_web_route; a tier-1
// folder (whose derived rules don't grant it) does not, exactly as ipc's
// registerRaw hid it before.
func TestWebRoutesMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	grantMCPTools(t, db, "hq", "set_web_route", "del_web_route", "list_web_routes")

	granted := listToolNames(t, serveWebRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq")))
	for _, name := range []string{"set_web_route", "del_web_route", "list_web_routes"} {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
	}
	ungranted := listToolNames(t, serveWebRoutesMCP(t, db, "world/a", "folder:world/a", deriveFolderGrants(db, "world/a")))
	if ungranted["set_web_route"] {
		t.Fatal("set_web_route visible to a folder not granted it")
	}
}

// TestWebRoutesMCP_GateDenies: a tool that is VISIBLE (a rule matches it) but
// DENIED by a later deny rule is rejected at call time — the injected tier gate
// (grants.CheckAction), not resreg's operator default, made the decision.
func TestWebRoutesMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	// callerSub="" isolates the CheckAction layer of the gate (skips db.Authorize).
	sock := serveWebRoutesMCP(t, db, "hq", "", []string{"*", "!set_web_route"})

	// Visible (the "*" rule matches) but call-denied by "!set_web_route".
	if !listToolNames(t, sock)["set_web_route"] {
		t.Fatal("set_web_route should be visible (a wildcard rule matches it)")
	}
	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/x", "access": "public",
	}); e == "" {
		t.Fatal("set_web_route should be denied by the tier gate")
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
	sock := serveWebRoutesMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolText(t, sock, "set_web_route", map[string]any{
		"path": "/pub/hq/app", "access": "public",
	}); e != "" {
		t.Fatalf("set_web_route errored: %s", e)
	}
	var n int
	err = db.SQL().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action='web_routes:create' AND outcome='ok' AND folder='hq' AND surface='mcp'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log has %d web_routes:create rows, want 1", n)
	}
}
