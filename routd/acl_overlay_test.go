package routd

import (
	"database/sql"
	"path/filepath"
	"slices"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// attachACLSibling attaches a migrated messages.db (real acl/acl_membership
// schema) as routd's sibling msgs handle and returns a *store.Store for
// seeding operator acl rows the way `arizuko grant` / authd would. This is
// the cross-DB read surface the split topology gives routd: the rows are
// owned (written) elsewhere; routd reads them RO via d.msgs. The DB name is
// unique per call (store.OpenMem shares one cache=shared DB process-wide,
// which would cross-contaminate acl rows between tests).
func attachACLSibling(t *testing.T, d *DB) *store.Store {
	t.Helper()
	h, err := sql.Open("sqlite", "file:aclsib_"+randHex(8)+"?mode=memory&cache=shared&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatalf("open acl sibling: %v", err)
	}
	if err := store.Migrate(h); err != nil {
		t.Fatalf("migrate acl sibling: %v", err)
	}
	d.msgs = h
	t.Cleanup(func() { h.Close() })
	return store.New(h)
}

func addACL(t *testing.T, s *store.Store, principal, action, scope, effect string) {
	t.Helper()
	if err := s.AddACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: effect,
	}); err != nil {
		t.Fatalf("AddACLRow %s %s %s: %v", principal, action, scope, err)
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
	s := attachACLSibling(t, db)

	const folder = "w/a/b/c" // tier 3: defaults = reply, send_file, like, edit
	addACL(t, s, "folder:"+folder, "mcp:send", folder, "allow")
	addACL(t, s, "folder:"+folder, "mcp:reply", folder, "deny")

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
	s := attachACLSibling(t, db)

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
	addACL(t, s, sub, "mcp:send", folder, "allow")
	if !db.Authorize(sub, folder, "mcp:send", nil) {
		t.Error("operator allow row should grant mcp:send")
	}

	// Operator deny row blocks a tool the tier default allows (deny wins).
	addACL(t, s, sub, "mcp:reply", folder, "deny")
	if db.Authorize(sub, folder, "mcp:reply", nil) {
		t.Error("operator deny row should block mcp:reply (deny wins)")
	}
}

// TestDBAuthorize_NoSiblingTierDefault: with no messages.db (no acl table),
// Authorize reduces to the tier-default fallback for mcp:* on the own folder
// — the existing in-process MCP path must keep working unchanged.
func TestDBAuthorize_NoSiblingTierDefault(t *testing.T) {
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
	s := attachACLSibling(t, db)

	const folder = "w/a/b/c"
	const jid = "slack:team/channel/c1"
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: folder}})
	if _, err := db.PutTurnContext("t1", folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	addACL(t, s, "folder:"+folder, "mcp:reply", folder, "deny")

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
	s := attachACLSibling(t, db)

	const folder = "w/a/b/c"
	const jid = "slack:team/channel/c1"
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: folder}})
	if _, err := db.PutTurnContext("t1", folder, "", jid, "u1", ""); err != nil {
		t.Fatal(err)
	}
	addACL(t, s, "folder:"+folder, "mcp:send", folder, "allow")

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
// it now reads the sibling messages.db.
func TestServeTurnMCP_ListACL(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := attachACLSibling(t, db)

	const folder = "w" // tier 0: list_acl is tier 0-1 only
	addACL(t, s, "folder:"+folder, "mcp:send", folder, "allow")
	addACL(t, s, "folder:"+folder, "mcp:reply", folder, "deny")
	// A row in a different scope must NOT appear (tool filters on scope==folder).
	addACL(t, s, "folder:other", "mcp:send", "other", "allow")

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
