package routd

import (
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// addACL seeds an operator acl row into routd's OWN routd.db — routd owns the acl
// tables (spec 5/5), so the evaluator (auth.Authorize / auth.EffectiveActions /
// db.UserScopes) reads them from there, not the sibling messages.db. A raw INSERT
// (not store.AddACLRow) so it doesn't depend on the audit_log table.
func addACL(t *testing.T, d *DB, principal, action, scope, effect string) {
	t.Helper()
	if _, err := d.SQL().Exec(
		`INSERT OR IGNORE INTO acl(principal, action, scope, effect, granted_at)
		 VALUES(?,?,?,?,?)`,
		principal, action, scope, effect, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed acl %s %s %s: %v", principal, action, scope, err)
	}
}

// holdsAction is the visibility view a test asserts against: auth.EffectiveActions
// over the folder's acl rows (role:member floor + delegated + operator rows).
func holdsAction(db *DB, folder, action string) bool {
	held := auth.EffectiveActions(store.New(db.SQL()), auth.Caller{Principal: "folder:" + folder})
	return held(action)
}

// TestIntegration_RoleInheritedGrant (5/33): a grant held via ROLE membership
// (acl_membership), not a direct folder:<path> row, is both VISIBLE
// (EffectiveActions expands Ancestors) and ALLOWED (auth.Authorize expands
// Ancestors) — the two reads agree.
func TestIntegration_RoleInheritedGrant(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	// A role grants mcp:post; the folder agent is a member of that role.
	addACL(t, db, "role:poster", "mcp:post", folder, "allow")
	st := store.New(db.SQL())
	if err := st.AddMembership("folder:"+folder, "role:poster", "test"); err != nil {
		t.Fatal(err)
	}

	if !holdsAction(db, folder, "mcp:post") {
		t.Fatal("role-inherited mcp:post missing from EffectiveActions")
	}
	if !db.Authorize("folder:"+folder, folder, "mcp:post", nil) {
		t.Error("live gate should also allow the role-inherited grant")
	}
}

// TestOperatorOverlay: operator acl rows (principal=folder:<folder>) overlay onto
// the role:member floor. An allow grants a non-floor tool; a deny blocks a floor
// verb (deny wins); floor verbs the operator didn't touch stay allowed.
func TestOperatorOverlay(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	const sub = "folder:" + folder
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil { // → role:member floor
		t.Fatal(err)
	}
	addACL(t, db, sub, "mcp:register_group", folder+"/**", "allow") // non-floor tool
	addACL(t, db, sub, "mcp:reply", "**", "deny")                   // block a floor verb

	if !holdsAction(db, folder, "mcp:register_group") {
		t.Error("overlay allow: register_group must be visible")
	}
	if !db.Authorize(sub, folder+"/child", "mcp:register_group", nil) {
		t.Error("overlay allow: register_group must be permitted in scope")
	}
	if db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("overlay deny: reply must be blocked (deny wins over the floor)")
	}
	// A floor verb the operator didn't touch is still allowed.
	if !db.Authorize(sub, folder, "mcp:send", nil) {
		t.Error("floor verb send must stay allowed")
	}
}

// TestDBAuthorize_MagnitudeFromMembership pins the 5/33 floor: magnitude comes from a
// folder's role:member membership (DATA seeded at register), NOT depth. An
// UNregistered folder (no membership) is denied — there is no tier default.
func TestDBAuthorize_MagnitudeFromMembership(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	// Unregistered folder → no membership → deny (no fallback).
	if db.Authorize("folder:w/a/b/c", "w/a/b/c", "mcp:reply", nil) {
		t.Error("unregistered folder (no membership) must be denied — no tier fallback")
	}
	// Register two folders at different depths; each gets the SAME floor (no tier).
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := db.PutGroup(core.Group{Folder: "w/a/b/c"}); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"demo", "w/a/b/c"} {
		for _, verb := range []string{"mcp:reply", "mcp:send", "mcp:like"} {
			if !db.Authorize("folder:"+f, f, verb, nil) {
				t.Errorf("role:member floor should allow %s for %s", verb, f)
			}
		}
		// A management tool is NOT floor — denied without an explicit delegation.
		if db.Authorize("folder:"+f, f, "mcp:register_group", nil) {
			t.Errorf("management tool must be denied for %s without delegation", f)
		}
	}
}

// TestACLReadsOwnDB proves routd evaluates ACL against its OWN routd.db: a deny row
// seeded there blocks a floor verb (deny wins). routd opens NO sibling DB.
func TestACLReadsOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	const sub = "folder:" + folder
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	if !db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("role:member should allow reply with no operator row")
	}
	addACL(t, db, sub, "mcp:reply", folder, "deny")
	if db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("routd.db deny row should block reply (deny wins)")
	}
}

// TestServeTurnMCP_OperatorDenyBlocksTool drives the wired socket: an operator deny
// row on mcp:reply blocks the reply tool the floor would allow.
func TestServeTurnMCP_OperatorDenyBlocksTool(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	const jid = "slack:team/channel/c1"
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil { // → role:member floor
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: folder}})
	if _, err := db.PutTurnContext("t1", folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	addACL(t, db, "folder:"+folder, "mcp:reply", "**", "deny")

	srv := NewServer(db, nil, &recDeliverer{pid: "pid-x"}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", folder)
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: folder, chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	sock := groupfolder.IpcSocket(ipcDir)
	_, errText := callToolOverSock(t, sock, "reply",
		map[string]any{"chatJid": jid, "text": "should be blocked"})
	if errText == "" {
		t.Fatal("reply should be denied by operator deny acl row, but succeeded")
	}
}

// TestServeTurnMCP_OperatorAllowGrantsTool: an allow row registers + permits a tool
// the floor does NOT grant. The folder is unregistered (no floor), so send is dark
// until the operator's allow row grants it.
func TestServeTurnMCP_OperatorAllowGrantsTool(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c"
	const jid = "slack:team/channel/c1"
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: folder}})
	if _, err := db.PutTurnContext("t1", folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	// No PutGroup → no role:member floor → send is dark until the allow row grants it.
	addACL(t, db, "folder:"+folder, "mcp:send", folder, "allow")

	deliver := &recDeliverer{pid: "pid-x"}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", folder)
	stop, err := srv.ServeTurnMCP(
		turnMCP{folder: folder, chatJID: jid, turnID: "t1", trigger: "u1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	sock := groupfolder.IpcSocket(ipcDir)
	_, errText := callToolOverSock(t, sock, "send",
		map[string]any{"chatJid": jid, "text": "allowed by operator row"})
	if errText != "" {
		t.Fatalf("send should be granted by operator allow row, got error: %s", errText)
	}
	if len(deliver.sends) != 1 || deliver.sends[0].text != "allowed by operator row" {
		t.Fatalf("deliver.sends=%+v want one 'allowed by operator row'", deliver.sends)
	}
}

// TestServeTurnMCP_ListACL returns the operator acl rows scoped to the folder.
// list_acl is a delegated grant (not floor); granted here scope ** so the grant row
// itself is filtered out of the folder-scoped list (only rows whose scope == the
// queried folder are returned).
func TestServeTurnMCP_ListACL(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w"
	addACL(t, db, "folder:"+folder, "mcp:list_acl", "**", "allow")
	addACL(t, db, "folder:"+folder, "mcp:send", folder, "allow")
	addACL(t, db, "folder:"+folder, "mcp:reply", folder, "deny")
	// A row in a different scope must NOT appear (tool filters on scope==folder).
	addACL(t, db, "folder:other", "mcp:send", "other", "allow")

	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc", folder)
	stop, err := srv.ServeTurnMCP(turnMCP{folder: folder, turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	defer stop()

	sock := groupfolder.IpcSocket(ipcDir)
	payload, errText := callToolOverSock(t, sock, "list_acl",
		map[string]any{"folder": folder})
	if errText != "" {
		t.Fatalf("list_acl error: %s", errText)
	}
	acl, ok := payload["acl"].([]any)
	if !ok {
		t.Fatalf("list_acl payload missing acl array: %v", payload)
	}
	if len(acl) != 2 {
		t.Fatalf("list_acl returned %d rows want 2 (scope-filtered): %v", len(acl), acl)
	}
}
