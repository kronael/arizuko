package routd

// Parity tests for the spec 5/16 acl migration: the agent's
// add_acl/remove_acl/list_acl now ride resreg's MCP mechanism through the
// ServeMCP postBuild seam instead of three hand-rolled ipc bodies. Each test
// drives the REAL unix socket so the seam + injected Gate (grants.CheckAction +
// db.Authorize + AuthorizeStructural scope-containment) + the handler's tx-aware
// grant/revoke (incl. the scope=="**"→membership overload with cycle check) +
// the Visible predicate are all exercised.
//
// ACL is permissions, so the scope-containment is security-critical:
// TestACLMCP_ContainmentDenied fails if the Gate's AuthorizeStructural is
// dropped (a folder would grant/revoke outside its authority, incl. "**").

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

func serveACLMCP(t *testing.T, db *DB, folder, callerSub string, rules []string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.aclPostBuild(folder, callerSub, rules, srv.db.Authorize, auth.Resolve(folder))
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

// serveACLMCPElevated stands up the agent socket as an operator /root turn
// would: the tier-0 `*` grant set, an allow-all row-ACL (turnAuthorize(true))
// and a tier-0 EFFECTIVE identity (turnIdentity(folder, true)) — the exact
// wiring ServeTurnMCP hands the postBuild for an elevated turn.
func serveACLMCPElevated(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	rules := []string{"*"}
	callerSub := "folder:" + folder
	pb := srv.aclPostBuild(folder, callerSub, rules, srv.turnAuthorize(true), turnIdentity(folder, true))
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

func aclRowCount(t *testing.T, db *DB, scope string) int {
	t.Helper()
	n := 0
	for _, r := range db.ListACL("") {
		// Exclude the seeded role:operator (*, **) base row (migration 0022, F1):
		// these helpers assert what a REST/MCP write produced, not the seed.
		if r.Principal == "role:operator" {
			continue
		}
		if r.Scope == scope {
			n++
		}
	}
	return n
}

func operatorMembershipExists(t *testing.T, db *DB, child string) bool {
	t.Helper()
	var n int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent='role:operator'`, child).Scan(&n); err != nil {
		t.Fatalf("membership query: %v", err)
	}
	return n > 0
}

// TestACLMCP_AddListRemove — happy path for a tier-1 world granted the acl tools:
// add_acl writes an acl row within its OWN world, list_acl shows it (tier 0-1
// only), remove_acl drops it. A tier-1 world is world-confined, so the scope
// stays under "root" (no folder resolves to tier 0 now — cross-world/`**` grants
// are the operator REST/CLI face). Exercises the seam + handler end-to-end.
func TestACLMCP_AddListRemove(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl", "remove_acl", "list_acl")
	sock := serveACLMCP(t, db, "root", "folder:root", deriveFolderGrants(db, "root"))

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team", "action": "read",
	}); e != "" {
		t.Fatalf("add_acl: %s", e)
	}
	if aclRowCount(t, db, "root/team") != 1 {
		t.Fatalf("add_acl did not write a row (root/team rows=%d)", aclRowCount(t, db, "root/team"))
	}
	res, e := callToolOverSock(t, sock, "list_acl", map[string]any{"folder": "root/team"})
	if e != "" {
		t.Fatalf("list_acl: %s", e)
	}
	if res["folder"] != "root/team" {
		t.Fatalf("list_acl returned unexpected shape: %v", res)
	}
	if _, e := callToolOverSock(t, sock, "remove_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team", "action": "read",
	}); e != "" {
		t.Fatalf("remove_acl: %s", e)
	}
	if aclRowCount(t, db, "root/team") != 0 {
		t.Fatal("remove_acl did not drop the row")
	}
}

// TestACLMCP_OperatorGrantDeniedViaAgent — the agent socket can NO LONGER grant
// the operator role: a tier-1 world granted add_acl (so CheckAction + db.Authorize
// pass) is still blocked by the Gate's containment when the scope is "**" (no
// folder owns the whole tree now). Granting `**` moved to the operator REST/CLI
// face (which carries the empty-folder operator identity). The scope=="**"→edge
// overload itself is covered by TestACLAddOperatorEndpoint on the REST twin.
func TestACLMCP_OperatorGrantDeniedViaAgent(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "root"})
	grantMCPTools(t, db, "root", "add_acl")
	sock := serveACLMCP(t, db, "root", "folder:root", deriveFolderGrants(db, "root"))

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:carol", "scope": "**",
	}); e == "" {
		t.Fatal("agent-socket add_acl ** must be denied (operator role is REST/CLI-only)")
	}
	if operatorMembershipExists(t, db, "google:carol") {
		t.Fatal("denied ** grant still wrote an operator membership edge")
	}
}

// TestACLMCP_ContainmentDenied — the security invariant. A TIER-1 folder
// ("world/a", one slash) granted add_acl via an operator ACL row (so CheckAction
// passes) is world-confined by the Gate's AuthorizeStructural (add_acl case):
// an own-world scope is allowed, but a CROSS-WORLD scope AND "**" are denied,
// nothing written. Fails if the containment is dropped.
func TestACLMCP_ContainmentDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/b", "other", "other/x"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	// Grant folder:world/a the add_acl tool. The scope must cover the caller's own
	// folder (the db.Authorize socket-folder check); the tool's `scope` arg is what
	// the containment (AuthorizeStructural) rules on.
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES ('folder:world/a', 'mcp:add_acl', 'world/a', 'allow', '', '', 'test', ?)`, nowTS()); err != nil {
		t.Fatal(err)
	}
	sock := serveACLMCP(t, db, "world/a", "folder:world/a", deriveFolderGrants(db, "world/a"))

	// Own world: allowed.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "world/b", "action": "read",
	}); e != "" {
		t.Fatalf("add_acl within own world should be allowed: %s", e)
	}
	// Cross-world: denied, nothing written.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "other/x", "action": "read",
	}); e == "" {
		t.Fatal("add_acl on a different world must be denied")
	}
	if aclRowCount(t, db, "other/x") != 0 {
		t.Fatal("denied add_acl still wrote a cross-world row")
	}
	// "**" operator role: denied (a tier-1 folder does not own **).
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "**",
	}); e == "" {
		t.Fatal("add_acl ** by a non-tier-0 folder must be denied")
	}
	if operatorMembershipExists(t, db, "google:x") {
		t.Fatal("denied ** grant still wrote an operator membership edge")
	}
}

// TestACLMCP_RootElevationGrantsOperatorRole is the elevated counterpart to
// TestACLMCP_OperatorGrantDeniedViaAgent: a tier-1 folder cannot grant scope
// "**" over the plain agent socket, but an operator /root turn from that SAME
// folder can — the structural gate sees tier 0 under elevation (turnIdentity),
// not the folder's static tier. Regression guard for the class of bug
// d452d6ef fixed (elevation reaching the row-ACL half but not the structural
// half of aclPostBuild's Gate).
func TestACLMCP_RootElevationGrantsOperatorRole(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "root"}) // tier 1 socket
	sock := serveACLMCPElevated(t, db, "root")

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:carol", "scope": "**",
	}); e != "" {
		t.Fatalf("/root add_acl ** should be allowed: %s", e)
	}
	if !operatorMembershipExists(t, db, "google:carol") {
		t.Fatal("/root ** grant did not write an operator membership edge")
	}
}
