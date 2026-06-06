package routd

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// addACL seeds an operator acl row into routd's OWN routd.db — routd owns the
// acl tables (spec 5/5), so the evaluator (deriveFolderGrants / db.Authorize /
// db.UserScopes) reads them from there, not the sibling messages.db. A raw
// INSERT (not store.AddACLRow) so it doesn't depend on the audit_log table the
// operator ACL-WRITE path needs — that write path is a separate follow-up.
func addACL(t *testing.T, d *DB, principal, action, scope, effect string) {
	t.Helper()
	if _, err := d.SQL().Exec(
		`INSERT OR IGNORE INTO acl(principal, action, scope, effect, granted_at)
		 VALUES(?,?,?,?,?)`,
		principal, action, scope, effect, "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("seed acl %s %s %s: %v", principal, action, scope, err)
	}
}

// TestDeriveFolderGrants_Overlay: the per-folder operator acl rows
// (principal=folder:<folder>, action=mcp:<tool>) overlay onto the tier
// defaults, with deny rendered as a `!rule`. Faithful to gated's
// runAgentWithOpts overlay.
func TestDeriveFolderGrants_Overlay(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c" // tier 3: defaults = reply, send_file, like, edit
	addACL(t, db, "folder:"+folder, "mcp:send", folder, "allow")
	addACL(t, db, "folder:"+folder, "mcp:reply", folder, "deny")

	rules := deriveFolderGrants(db, folder)
	if !slices.Contains(rules, "send") {
		t.Errorf("overlay missing allow rule 'send': %v", rules)
	}
	if !slices.Contains(rules, "!reply") {
		t.Errorf("overlay missing deny rule '!reply': %v", rules)
	}
	// Tier default still present (overlay appends, doesn't replace).
	if !slices.Contains(rules, "send_file") {
		t.Errorf("tier default 'send_file' dropped after overlay: %v", rules)
	}
}

// TestDBAuthorize_RowOverrides: the per-call row-ACL check (db.Authorize,
// wired into ServeMCP's authorizeCall) honours operator allow/deny rows on
// top of the tier-default fallback. This is the check that was OFF in routd
// (callerSub="" short-circuited authorizeCall to true).
func TestDBAuthorize_RowOverrides(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c" // tier 3: reply allowed by default, send denied
	const sub = "folder:" + folder

	// No rows yet: tier-default fallback. reply allowed, send denied.
	if !db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("tier-3 default should allow mcp:reply")
	}
	if db.Authorize(sub, folder, "mcp:send", nil) {
		t.Error("tier-3 default should deny mcp:send")
	}

	// Operator allow row grants a tool the tier default denies.
	addACL(t, db, sub, "mcp:send", folder, "allow")
	if !db.Authorize(sub, folder, "mcp:send", nil) {
		t.Error("operator allow row should grant mcp:send")
	}

	// Operator deny row blocks a tool the tier default allows (deny wins).
	addACL(t, db, sub, "mcp:reply", folder, "deny")
	if db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("operator deny row should block mcp:reply (deny wins)")
	}
}

// TestDBAuthorize_EmptyACLTierDefault: with an empty acl table (no operator
// rows), Authorize reduces to the tier-default fallback for mcp:* on the own
// folder — the in-process MCP path keeps working unchanged.
func TestDBAuthorize_EmptyACLTierDefault(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	if !db.Authorize("folder:demo", "demo", "mcp:send", nil) {
		t.Error("tier-0 default (no acl table) should allow mcp:send")
	}
	if !db.Authorize("folder:w/a/b/c", "w/a/b/c", "mcp:reply", nil) {
		t.Error("tier-3 default (no acl table) should allow mcp:reply")
	}
	if db.Authorize("folder:w/a/b/c", "w/a/b/c", "mcp:send", nil) {
		t.Error("tier-3 default (no acl table) should deny mcp:send")
	}
}

// TestACLReadsOwnDB proves routd evaluates ACL against its OWN routd.db: a deny
// row seeded there blocks reply (deny wins) for both db.Authorize and
// deriveFolderGrants. routd opens NO sibling DB — ACL lives only in routd.db.
func TestACLReadsOwnDB(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w/a/b/c" // tier 3: reply allowed by default
	const sub = "folder:" + folder

	// No operator row → tier-3 default allows reply.
	if !db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("tier-3 default should allow reply with no acl row")
	}
	if slices.Contains(deriveFolderGrants(db, folder), "!reply") {
		t.Error("deriveFolderGrants must not deny with no acl row")
	}

	// A deny in routd's OWN db DOES apply (deny wins).
	addACL(t, db, sub, "mcp:reply", folder, "deny")
	if db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("routd.db deny row should block reply (deny wins)")
	}
	if !slices.Contains(deriveFolderGrants(db, folder), "!reply") {
		t.Error("deriveFolderGrants must apply the routd.db deny row")
	}
}

// TestServeTurnMCP_OperatorDenyBlocksTool drives the wired socket: an
// operator deny row on mcp:reply blocks the reply tool the tier default
// would allow. Proves the overlay + per-call Authorize fire over the real
// MCP socket (the G10 fix), not just in the renderer.
func TestServeTurnMCP_OperatorDenyBlocksTool(t *testing.T) {
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
	addACL(t, db, "folder:"+folder, "mcp:reply", folder, "deny")

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

// TestServeTurnMCP_OperatorAllowGrantsTool: an allow row registers + permits
// a tool the tier default denies (send at tier 3). Without the overlay the
// tool would be dark; without per-call Authorize the row would be ignored.
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

// TestServeTurnMCP_ListACL returns the operator acl rows scoped to the
// folder. StoreFns.ListACL was nil in routd → the list_acl tool was dark;
// it now reads routd's OWN routd.db acl table.
func TestServeTurnMCP_ListACL(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const folder = "w" // tier 0: list_acl is tier 0-1 only
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
