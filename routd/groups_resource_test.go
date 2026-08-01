package routd

// Parity tests for the spec 5/16 groups fold: the agent's register_group +
// refresh_groups now ride resreg's MCP mechanism through the ServeMCP postBuild seam
// instead of two hand-rolled ipc bodies. register_group is a FORWARDER (its group
// row + route + git-init FS side-effects can't ride a resreg tx), so its auth (one
// auth.Authorize on the CHILD folder) rides Authz and its audit rides s.audit. Each
// test drives the REAL unix socket end-to-end so the seam + Authz containment + the
// handler's spawn cap + the visibility predicate are all exercised.
//
// 4/R: neither group tool is in the role:member floor — every test that expects one to
// work delegates it explicitly with grantMCPTools (scope <folder>/**, which covers the
// folder itself and every descendant it may register under).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// serveGroupsMCP stands up the agent socket for folder + the groups resreg seam,
// sets groupsDir so register_group can git-init the group dir, and returns the
// socket path. Authz reads the folder's acl rows (grant via grantMCPTools).
func serveGroupsMCP(t *testing.T, db *DB, folder, callerSub, groupsDir string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetDirs(groupsDir, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.groupsPostBuild(folder, callerSub, srv.db.Authorize,
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

// serveGroupsMCPElevated stands up the agent socket as an operator /root turn
// would: an allow-all row-ACL (turnAuthorize(true)), an all-visible view and a ROOT
// effective identity (turnIdentity(folder, true)) — the exact wiring ServeTurnMCP
// hands the postBuild for an elevated turn.
func serveGroupsMCPElevated(t *testing.T, db *DB, folder, groupsDir string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetDirs(groupsDir, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	callerSub := "folder:" + folder
	pb := srv.groupsPostBuild(folder, callerSub, srv.turnAuthorize(true),
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

// TestGroupsMCP_RegisterCreatesChild: a folder delegated register_group over its own
// subtree registers a direct child → the group row lands, a room-matched default
// route lands, and the group dir is git-inited (the s.registerGroup side-effects,
// preserved through the forwarder).
func TestGroupsMCP_RegisterCreatesChild(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	groupsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(groupsDir, "world/team/child"), 0o755); err != nil {
		t.Fatal(err)
	}
	grantMCPTools(t, db, "world/team", "register_group")
	sock := serveGroupsMCP(t, db, "world/team", "folder:world/team", groupsDir)

	text, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:900", "folder": "world/team/child"})
	if e != "" {
		t.Fatalf("register_group errored: %s", e)
	}
	if text != `{"folder":"world/team/child","jid":"telegram:900","registered":true}` {
		t.Fatalf("register result = %q", text)
	}
	if !db.GroupExists("world/team/child") {
		t.Fatal("group row not persisted")
	}
	routes, _ := db.Routes()
	var found bool
	for _, r := range routes {
		if r.Match == "room=900" && r.Target == "world/team/child" {
			found = true
		}
	}
	if !found {
		t.Fatalf("room route not added; routes=%+v", routes)
	}
	if _, err := os.Stat(filepath.Join(groupsDir, "world/team/child", ".git")); err != nil {
		t.Fatalf("group dir not git-inited: %v", err)
	}
}

// TestGroupsMCP_RegisterOutsideSubtreeDenied is the crux: a folder delegated
// register_group over world/team/** may only register inside that subtree.
// "world/team" registering "world/other/child" (a sibling subtree) is denied by the
// Authz containment (auth.Authorize on the CHILD folder), and no group row is
// written. Dropping the authorize call from groupsPostBuild's Authz flips this to a
// success — so this test fails-on-broken, guarding the containment.
func TestGroupsMCP_RegisterOutsideSubtreeDenied(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	grantMCPTools(t, db, "world/team", "register_group")
	sock := serveGroupsMCP(t, db, "world/team", "folder:world/team", t.TempDir())

	if _, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:901", "folder": "world/other/child"}); e == "" {
		t.Fatal("registering outside the caller's subtree should be denied")
	}
	if db.GroupExists("world/other/child") {
		t.Fatal("denied register still wrote a group row")
	}
	// Sanity: the SAME caller CAN register a direct child, so the denial is the
	// containment, not a blanket failure.
	if _, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:902", "folder": "world/team/ok"}); e != "" {
		t.Fatalf("direct-child register should succeed: %s", e)
	}
}

// TestGroupsMCP_RootElevationRegistersUngranted is the elevated counterpart to
// TestGroupsMCP_RegisterOutsideSubtreeDenied: a folder holding NO register_group
// grant cannot register anything over the plain agent socket, but an operator /root
// turn from that SAME folder can — elevation swaps in the allow-all authorize.
// Regression guard for the class of bug d452d6ef fixed.
func TestGroupsMCP_RootElevationRegistersUngranted(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	// world/a/b is deliberately NOT a registered group and holds no grants: that
	// keeps the handler's spawn cap out of play (it only fires when the caller's OWN
	// folder is a registered parent — see TestGroupsMCP_SpawnCapFires), so this test
	// isolates the elevation containment cleanly.
	groupsDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(groupsDir, "world/a/b/child"), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := serveGroupsMCPElevated(t, db, "world/a/b", groupsDir)

	if _, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:905", "folder": "world/a/b/child"}); e != "" {
		t.Fatalf("/root register_group without a grant should be allowed: %s", e)
	}
	if !db.GroupExists("world/a/b/child") {
		t.Fatal("/root register_group did not write the group row")
	}
}

// TestGroupsMCP_SpawnCapFires: the handler's spawn cap (auth.CheckSpawnAllowed) still
// bites — a parent at max_children=1 with one existing child cannot register a
// second. Containment passes (the target is a direct child), so this isolates the
// handler-side max_children guard.
func TestGroupsMCP_SpawnCapFires(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/team", Config: core.GroupConfig{MaxChildren: 1}})
	_ = db.PutGroup(core.Group{Folder: "world/team/existing"})
	grantMCPTools(t, db, "world/team", "register_group")
	sock := serveGroupsMCP(t, db, "world/team", "folder:world/team", t.TempDir())

	if _, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:903", "folder": "world/team/second"}); e == "" {
		t.Fatal("register past max_children should be denied by the spawn cap")
	}
	if db.GroupExists("world/team/second") {
		t.Fatal("spawn-cap-denied register still wrote a group row")
	}
}

// TestGroupsMCP_RefreshGroupsLists: refresh_groups returns a folder row for every
// registered group (unscoped, matching the deleted body). Driven over the socket so
// the list handler + forwarder read path are exercised.
func TestGroupsMCP_RefreshGroupsLists(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	_ = db.PutGroup(core.Group{Folder: "solo/inbox"})
	grantMCPTools(t, db, "world", "refresh_groups")
	sock := serveGroupsMCP(t, db, "world", "folder:world", t.TempDir())

	text, e := callToolText(t, sock, "refresh_groups", nil)
	if e != "" {
		t.Fatalf("refresh_groups errored: %s", e)
	}
	var out []struct {
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("refresh payload %q not JSON: %v", text, err)
	}
	seen := map[string]bool{}
	for _, g := range out {
		seen[g.Folder] = true
	}
	for _, f := range []string{"world", "world/a", "solo/inbox"} {
		if !seen[f] {
			t.Fatalf("refresh_groups missing %q; got %s", f, text)
		}
	}
}

// TestGroupsMCP_Visibility: the visibility predicate (auth.EffectiveActions over the
// caller's acl rows) gates tools/list. Neither group tool is in the role:member
// floor, so a folder sees each one only when it was delegated it — and a folder with
// no delegation (any depth) sees neither.
func TestGroupsMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	_ = db.PutGroup(core.Group{Folder: "world/a/b"})
	grantMCPTools(t, db, "hq", "register_group", "refresh_groups")
	grantMCPTools(t, db, "world/a", "refresh_groups")

	// Both delegated → both visible.
	both := listToolNames(t, serveGroupsMCP(t, db, "hq", "folder:hq", t.TempDir()))
	if !both["register_group"] || !both["refresh_groups"] {
		t.Fatalf("a folder granted both should see both; got register=%v refresh=%v",
			both["register_group"], both["refresh_groups"])
	}

	// Only refresh_groups delegated → register_group stays hidden.
	partial := listToolNames(t, serveGroupsMCP(t, db, "world/a", "folder:world/a", t.TempDir()))
	if partial["register_group"] {
		t.Fatal("register_group visible to a folder granted only refresh_groups")
	}
	if !partial["refresh_groups"] {
		t.Fatal("refresh_groups not visible to a folder granted it")
	}

	// No delegation → neither (the role:member floor carries no group tool).
	none := listToolNames(t, serveGroupsMCP(t, db, "world/a/b", "folder:world/a/b", t.TempDir()))
	if none["register_group"] || none["refresh_groups"] {
		t.Fatalf("an ungranted folder must see no group tools; got register=%v refresh=%v",
			none["register_group"], none["refresh_groups"])
	}
}

// TestGroupsMCP_AuditRowLands: an agent register_group emits one register_group
// system-audit event through routd's Audit sink (the forwarder equivalent of the
// deleted emitSys — resreg writes no audit_log row for a forwarder). The event
// targets the CHILD folder and carries outcome ok.
func TestGroupsMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	auditDir := t.TempDir()
	grantMCPTools(t, db, "world/team", "register_group")
	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetDirs(t.TempDir(), "")
	srv.SetAudit(audit.New(audit.Config{Enabled: true, DataDir: auditDir, Instance: "test"}))

	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.groupsPostBuild("world/team", "folder:world/team", srv.db.Authorize,
		agentVisibleFor(srv, "folder:world/team", false), auth.Resolve("world/team"))
	stop, err := ipc.ServeMCP(sock, srv.buildGatedFns(turnMCP{folder: "world/team"}),
		srv.buildStoreFns(turnMCP{folder: "world/team"}), "world/team", false, 0, "folder:world/team", pb)
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	t.Cleanup(stop)
	for deadline := time.Now().Add(2 * time.Second); !fileExists(sock) && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}

	if _, e := callToolText(t, sock, "register_group",
		map[string]any{"jid": "telegram:904", "folder": "world/team/child"}); e != "" {
		t.Fatalf("register_group errored: %s", e)
	}

	raw, err := os.ReadFile(filepath.Join(auditDir, "audit-system.jl"))
	if err != nil {
		t.Fatalf("read audit-system.jl: %v", err)
	}
	var ev audit.SystemEvent
	if err := json.Unmarshal(trimLastLine(raw), &ev); err != nil {
		t.Fatalf("audit line %q not a SystemEvent: %v", raw, err)
	}
	if ev.Tool != "register_group" || ev.Folder != "world/team/child" || ev.Outcome.Status != "ok" {
		t.Fatalf("audit event = %+v, want tool=register_group folder=world/team/child ok", ev)
	}
}

// trimLastLine returns the last non-empty JSONL line of b (EmitSystem appends one
// line per event synchronously; register_group emits exactly one).
func trimLastLine(b []byte) []byte {
	s := b
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}
