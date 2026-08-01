package routd

// Parity tests for the spec 5/16 acl migration: the agent's
// add_acl/remove_acl/list_acl now ride resreg's MCP mechanism through the
// ServeMCP postBuild seam instead of three hand-rolled ipc bodies. Each test
// drives the REAL unix socket so the seam + injected Gate (auth.Authorize on the
// scope arg + auth.Delegate's subset-of-held check) + the handler's tx-aware
// grant/revoke (incl. the scope=="**"→membership overload with cycle check) +
// the Visible predicate are all exercised.
//
// ACL is permissions, so the scope-containment is security-critical:
// TestACLMCP_ContainmentDenied fails if the Gate stops binding the `scope` arg to
// the caller's grant (a folder would grant/revoke outside its authority, incl. "**").

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveACLMCP stands up the agent socket for folder + the acl resreg seam. Authz
// reads the folder's acl rows (grant via grantMCPTools or an explicit AddACLRow);
// no rules bundle.
func serveACLMCP(t *testing.T, db *DB, folder, callerSub string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	pb := srv.aclPostBuild(folder, callerSub, srv.db.Authorize,
		agentVisibleFor(srv, callerSub, false), auth.Resolve(folder))
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

// serveACLMCPElevated stands up the agent socket as an operator /root turn would:
// an allow-all row-ACL (turnAuthorize(true)), an all-visible view and a ROOT
// effective identity (turnIdentity(folder, true)) — the exact wiring ServeTurnMCP
// hands the postBuild for an elevated turn.
func serveACLMCPElevated(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	sock := groupfolder.IpcSocket(t.TempDir())
	callerSub := "folder:" + folder
	pb := srv.aclPostBuild(folder, callerSub, srv.turnAuthorize(true),
		agentVisibleFor(srv, callerSub, true), turnIdentity(folder, true))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: folder}),
		srv.buildStoreFns(turnMCP{folder: folder}), folder, true, 0, callerSub, pb)
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
		// Exclude seeded role:* infrastructure (role:operator from migration 0022,
		// role:member's messaging floor from 0023) — these helpers assert what a
		// REST/MCP WRITE produced, not the seeds.
		if strings.HasPrefix(r.Principal, "role:") {
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

// TestACLMCP_AddListRemove — happy path for a folder DELEGATED the acl tools over
// its own subtree: add_acl writes an acl row inside that subtree, list_acl shows
// it, remove_acl drops it. Cross-subtree and `**` grants stay the operator
// REST/CLI face. Exercises the seam + handler end-to-end.
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
	// 4/R: folder:root was DELEGATED "read" over its subtree WITH the grant option,
	// so it may re-delegate a subset (auth.Delegate). Without this the add is refused.
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option)
		 VALUES ('folder:root', 'read', 'root/**', 'allow', '', '', 'test', ?, 1)`, nowTS()); err != nil {
		t.Fatal(err)
	}
	sock := serveACLMCP(t, db, "root", "folder:root")

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
// the operator role: a folder delegated add_acl over its OWN subtree (root/**) is
// still blocked when the scope arg is "**", which that grant does not cover.
// Granting `**` moved to the operator REST/CLI face (which carries the empty-folder
// operator identity). The scope=="**"→edge overload itself is covered by
// TestACLAddOperatorEndpoint on the REST twin.
func TestACLMCP_OperatorGrantDeniedViaAgent(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "root"})
	grantMCPTools(t, db, "root", "add_acl")
	sock := serveACLMCP(t, db, "root", "folder:root")

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:carol", "scope": "**",
	}); e == "" {
		t.Fatal("agent-socket add_acl ** must be denied (operator role is REST/CLI-only)")
	}
	if operatorMembershipExists(t, db, "google:carol") {
		t.Fatal("denied ** grant still wrote an operator membership edge")
	}
}

// TestACLMCP_ContainmentDenied — the security invariant. A folder ("world/a")
// delegated add_acl over the `world` subtree may grant anywhere INSIDE world/**,
// but a CROSS-SUBTREE scope AND "**" are denied, nothing written. Containment IS
// the grant's scope (4/R decision 8) — fails if the Gate stops binding the `scope`
// arg to it.
func TestACLMCP_ContainmentDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/b", "other", "other/x"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	// Delegate folder:world/a the add_acl tool over the `world` subtree. That scope
	// is exactly the caller's authority: the tool's `scope` arg must fall inside it.
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES ('folder:world/a', 'mcp:add_acl', 'world/**', 'allow', '', '', 'test', ?)`, nowTS()); err != nil {
		t.Fatal(err)
	}
	// 4/R: folder:world/a was delegated "read" over world/** WITH the grant option —
	// so it may re-delegate read within world/**, and the auth.Delegate scope-check
	// itself refuses a cross-subtree scope (other/x), reinforcing the denial below.
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at, grant_option)
		 VALUES ('folder:world/a', 'read', 'world/**', 'allow', '', '', 'test', ?, 1)`, nowTS()); err != nil {
		t.Fatal(err)
	}
	sock := serveACLMCP(t, db, "world/a", "folder:world/a")

	// Inside the delegated subtree: allowed.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "world/b", "action": "read",
	}); e != "" {
		t.Fatalf("add_acl within the delegated subtree should be allowed: %s", e)
	}
	// Outside it: denied, nothing written.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "other/x", "action": "read",
	}); e == "" {
		t.Fatal("add_acl outside the delegated subtree must be denied")
	}
	if aclRowCount(t, db, "other/x") != 0 {
		t.Fatal("denied add_acl still wrote a cross-subtree row")
	}
	// "**" operator role: denied (the world/** grant does not cover **).
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "**",
	}); e == "" {
		t.Fatal("add_acl ** by a subtree-scoped folder must be denied")
	}
	if operatorMembershipExists(t, db, "google:x") {
		t.Fatal("denied ** grant still wrote an operator membership edge")
	}
}

// TestACLMCP_RootElevationGrantsOperatorRole is the elevated counterpart to
// TestACLMCP_OperatorGrantDeniedViaAgent: a subtree-scoped folder cannot grant
// scope "**" over the plain agent socket, but an operator /root turn from that
// SAME folder can — elevation reaches BOTH halves of aclPostBuild's Gate (the
// allow-all authorize AND the root identity that skips auth.Delegate). Regression
// guard for the class of bug d452d6ef fixed.
func TestACLMCP_RootElevationGrantsOperatorRole(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "root"})
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
