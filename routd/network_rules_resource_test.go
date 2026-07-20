package routd

// Parity tests for the spec 5/16 network_rules migration: the agent's
// network_allow/network_deny/network_list now ride resreg's MCP mechanism
// through the ServeMCP postBuild seam instead of hand-rolled ipc bodies. Each
// test drives the REAL unix socket end-to-end (not the handler directly) so the
// seam + injected Gate (grants.CheckAction + AuthorizeStructural tier cap +
// db.Authorize) + Visible predicate are all exercised.
//
// Tier note: these tools are granted only at tier 0 by default (DeriveRules
// tier-0 = ["*"]); tiers 1+ don't get them. Egress management also carries a
// STRUCTURAL tier cap (auth.AuthorizeStructural: tier 2+ denied) that operator
// ACL grants can't widen — TestNetworkRulesMCP_TierGateDeniesTier2 is the one
// that fails if that cap is dropped (the naive "pilot-shape Gate" regression).

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/ipc"
)

// grantMCPTools grants folder's agent-socket principal (folder:<folder>) the
// named mcp tools explicitly. Top-level worlds are tier 1 now (not the tier-0
// that got `*` by default), so a test that exercises a management tool's
// mechanics grants it here rather than leaning on a tier-0 default. Shared across
// the resource-parity tests.
func grantMCPTools(t *testing.T, db *DB, folder string, tools ...string) {
	t.Helper()
	for _, tl := range tools {
		if err := db.AddACLRow(core.ACLRow{
			Principal: "folder:" + folder, Action: "mcp:" + tl, Scope: folder, Effect: "allow",
		}); err != nil {
			t.Fatalf("grant %s to %s: %v", tl, folder, err)
		}
	}
}

// serveNetworkMCP stands up the agent socket for folder with the given grant
// rules + the network_rules resreg seam, and returns the socket path.
func serveNetworkMCP(t *testing.T, db *DB, folder, callerSub string, rules []string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	pb := srv.networkRulesPostBuild(folder, callerSub, rules, srv.db.Authorize, auth.Resolve(folder))
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

// TestNetworkRulesMCP_AllowListDeny: happy path for a tier-0 folder — allow
// appends a rule, list returns it in both the resolved set and the folder's own
// set, deny removes it.
func TestNetworkRulesMCP_AllowListDeny(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "network_allow", "network_deny", "network_list")
	rules := deriveFolderGrants(db, "hq")
	sock := serveNetworkMCP(t, db, "hq", "folder:hq", rules)

	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "example.com"}); e != "" {
		t.Fatalf("network_allow errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("hq"); len(got) != 1 || got[0].Target != "example.com" {
		t.Fatalf("rule not persisted under folder: %+v", got)
	}

	out, e := callToolOverSock(t, sock, "network_list", nil)
	if e != "" {
		t.Fatalf("network_list errored: %s", e)
	}
	if out["folder"] != "hq" {
		t.Fatalf("network_list folder = %v, want hq", out["folder"])
	}
	own, _ := out["own"].([]any)
	if len(own) != 1 {
		t.Fatalf("own = %v, want 1 rule", out["own"])
	}
	if m, _ := own[0].(map[string]any); m["folder"] != "hq" || m["target"] != "example.com" {
		t.Fatalf("own[0] = %v, want {folder:hq, target:example.com}", own[0])
	}
	resolved, _ := out["resolved"].([]any)
	if !sliceHasStr(resolved, "example.com") {
		t.Fatalf("resolved = %v, want to contain example.com", out["resolved"])
	}

	if _, e := callToolText(t, sock, "network_deny", map[string]any{"host": "example.com"}); e != "" {
		t.Fatalf("network_deny errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("hq"); len(got) != 0 {
		t.Fatalf("rules after deny = %v, want empty", got)
	}
}

// TestNetworkRulesMCP_BadHostRejected: hostname validation is preserved — a host
// with a scheme/path is rejected before it can persist; a *.glob is normalized to
// the apex; bare * (allow-all) stays rejected.
func TestNetworkRulesMCP_BadHostRejected(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "network_allow", "network_deny", "network_list")
	sock := serveNetworkMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "https://evil.com/x"}); e == "" {
		t.Fatal("network_allow with a non-bare host should be rejected")
	}
	if got, _ := db.ListNetworkRules("hq"); len(got) != 0 {
		t.Fatalf("invalid host must not persist; rules = %v", got)
	}

	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "*.api.example.com"}); e != "" {
		t.Fatalf("network_allow (*.glob) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("hq"); len(got) != 1 || got[0].Target != "api.example.com" {
		t.Fatalf("*.glob stored as %v, want [api.example.com]", got)
	}

	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "*"}); e == "" {
		t.Fatal("bare * (allow-all egress) should be rejected")
	}
}

// TestNetworkRulesMCP_DescendantTargeting: the documented egress-escalation path —
// a tier-0 caller may open egress for a DESCENDANT folder via the `folder` target
// arg (socket `world` writes a rule for `world/a`). Omitting `folder` defaults to
// the caller's own folder.
func TestNetworkRulesMCP_DescendantTargeting(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world"})   // tier 1 (granted the tool)
	_ = db.PutGroup(core.Group{Folder: "world/a"}) // descendant target
	grantMCPTools(t, db, "world", "network_allow", "network_deny", "network_list")
	sock := serveNetworkMCP(t, db, "world", "folder:world", deriveFolderGrants(db, "world"))

	// Target a descendant explicitly — the rule lands there, not on the socket.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world/a", "host": "example.com",
	}); e != "" {
		t.Fatalf("network_allow (world → world/a) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("world/a"); len(got) != 1 || got[0].Target != "example.com" {
		t.Fatalf("rule should land on target world/a: %+v", got)
	}
	if got, _ := db.ListNetworkRules("world"); len(got) != 0 {
		t.Fatalf("rule must not land on the socket folder: %+v", got)
	}

	// Omitting folder defaults to the caller's own folder.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "self.example.com"}); e != "" {
		t.Fatalf("network_allow (self, no folder) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("world"); len(got) != 1 || got[0].Target != "self.example.com" {
		t.Fatalf("omitted folder should default to the socket folder: %+v", got)
	}

	// deny targets the descendant too.
	if _, e := callToolText(t, sock, "network_deny", map[string]any{
		"folder": "world/a", "host": "example.com",
	}); e != "" {
		t.Fatalf("network_deny (world → world/a) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("world/a"); len(got) != 0 {
		t.Fatalf("descendant rule not removed: %+v", got)
	}
}

// TestNetworkRulesMCP_Tier1SubtreeContainment: a tier-1 caller (granted the tool)
// may target its own folder or a descendant, but NOT a sibling or its parent —
// the AuthorizeStructural containment on the ARG folder. Nothing writes on denial.
func TestNetworkRulesMCP_Tier1SubtreeContainment(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/a/x", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	// grant tier-1 world/a the tool (tiers 1+ lack it by default).
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:world/a", Action: "mcp:network_allow",
		Scope: "world/a", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	rules := deriveFolderGrants(db, "world/a")
	sock := serveNetworkMCP(t, db, "world/a", "folder:world/a", rules)

	// descendant: allowed.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world/a/x", "host": "example.com",
	}); e != "" {
		t.Fatalf("tier-1 → descendant should be allowed: %s", e)
	}
	if got, _ := db.ListNetworkRules("world/a/x"); len(got) != 1 {
		t.Fatalf("descendant rule not written: %+v", got)
	}
	// sibling: denied, nothing written.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world/b", "host": "example.com",
	}); e == "" {
		t.Fatal("tier-1 → sibling must be denied")
	}
	if got, _ := db.ListNetworkRules("world/b"); len(got) != 0 {
		t.Fatalf("sibling target must not write: %+v", got)
	}
	// parent: denied.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world", "host": "example.com",
	}); e == "" {
		t.Fatal("tier-1 → parent must be denied")
	}
	if got, _ := db.ListNetworkRules("world"); len(got) != 0 {
		t.Fatalf("parent target must not write: %+v", got)
	}
}

// TestNetworkRulesMCP_TierGateDeniesTier2: egress management carries a structural
// tier cap — tier 2+ can NEVER manage egress, even when an operator ACL row
// grants the tool (which passes both grants.CheckAction and db.Authorize). This
// fails on a Gate that only runs CheckAction + db.Authorize (the pilot shape) and
// drops auth.AuthorizeStructural.
func TestNetworkRulesMCP_TierGateDeniesTier2(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/a/b"}) // tier 2
	// Operator grants the tool: overlays into the derived rules AND makes
	// db.Authorize allow — so only the structural tier cap can still deny.
	if err := db.AddACLRow(core.ACLRow{
		Principal: "folder:world/a/b", Action: "mcp:network_allow",
		Scope: "world/a/b", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	rules := deriveFolderGrants(db, "world/a/b")
	sock := serveNetworkMCP(t, db, "world/a/b", "folder:world/a/b", rules)

	// Visible (the operator grant matches) but call-denied by the tier cap.
	if !listToolNames(t, sock)["network_allow"] {
		t.Fatal("network_allow should be visible (operator granted it)")
	}
	// Own folder: denied (tier 2+ can't manage egress).
	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "example.com"}); e == "" {
		t.Fatal("tier-2 network_allow (own folder) must be denied by the structural tier cap")
	}
	// A sibling target arg is denied too — tier 2 can't manage egress anywhere.
	_ = db.PutGroup(core.Group{Folder: "world/a/c"})
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world/a/c", "host": "example.com",
	}); e == "" {
		t.Fatal("tier-2 network_allow (sibling target) must be denied")
	}
	if got, _ := db.ListNetworkRules("world/a/b"); len(got) != 0 {
		t.Fatalf("denied tier-2 allow still wrote a rule: %+v", got)
	}
	if got, _ := db.ListNetworkRules("world/a/c"); len(got) != 0 {
		t.Fatalf("denied tier-2 sibling allow still wrote a rule: %+v", got)
	}
}

// serveNetworkMCPElevated stands up the agent socket as an operator /root turn
// would: the tier-0 `*` grant set, an allow-all row-ACL (turnAuthorize(true)) and a
// tier-0 EFFECTIVE identity (turnIdentity(folder, true)) — the exact wiring
// ServeTurnMCP hands the postBuild for an elevated turn.
func serveNetworkMCPElevated(t *testing.T, db *DB, folder string) string {
	t.Helper()
	srv := NewServer(db, nil, nil, nil, 0, "")
	ipcDir := t.TempDir()
	sock := groupfolder.IpcSocket(ipcDir)
	rules := []string{"*"}
	callerSub := "folder:" + folder
	pb := srv.networkRulesPostBuild(folder, callerSub, rules, srv.turnAuthorize(true), turnIdentity(folder, true))
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

// TestNetworkRulesMCP_RootElevationManagesEgress: the counterpart to the tier-2
// denial — an operator /root turn from a tier-2 folder CAN manage egress, for its
// own folder AND any subtree target, because the structural gate sees tier 0 under
// elevation (turnIdentity). This is the bug the rhias operator hit: /root
// network_allow('rhias/content','krons.fiu.wtf') 403'd "tier 2 cannot manage
// egress" because elevation reached the row-ACL gate (turnAuthorize) but not this
// structural gate.
func TestNetworkRulesMCP_RootElevationManagesEgress(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "world/a/b"}) // tier 2 socket
	sock := serveNetworkMCPElevated(t, db, "world/a/b")

	// Own folder: allowed under /root.
	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "krons.fiu.wtf"}); e != "" {
		t.Fatalf("/root network_allow (own folder) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("world/a/b"); len(got) != 1 || got[0].Target != "krons.fiu.wtf" {
		t.Fatalf("rule not persisted: %+v", got)
	}
	// A subtree target: allowed under /root (tier 0 is unrestricted).
	if _, e := callToolText(t, sock, "network_allow", map[string]any{
		"folder": "world/a/b/c", "host": "example.com",
	}); e != "" {
		t.Fatalf("/root network_allow (subtree target) errored: %s", e)
	}
	if got, _ := db.ListNetworkRules("world/a/b/c"); len(got) != 1 || got[0].Target != "example.com" {
		t.Fatalf("subtree rule not persisted: %+v", got)
	}
}

// TestNetworkRulesMCP_Visibility: the Visible predicate (MatchingRules) preserves
// tools/list gating — a tier-0 folder sees the tools; a tier-1 folder (whose
// derived rules don't grant them) does not.
func TestNetworkRulesMCP_Visibility(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	_ = db.PutGroup(core.Group{Folder: "world/a"})
	grantMCPTools(t, db, "hq", "network_allow", "network_deny", "network_list")

	granted := listToolNames(t, serveNetworkMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq")))
	for _, name := range []string{"network_allow", "network_deny", "network_list"} {
		if !granted[name] {
			t.Fatalf("%s not visible to a folder granted it", name)
		}
	}
	ungranted := listToolNames(t, serveNetworkMCP(t, db, "world/a", "folder:world/a", deriveFolderGrants(db, "world/a")))
	if ungranted["network_allow"] {
		t.Fatal("network_allow visible to a folder not granted it")
	}
}

// TestNetworkRulesMCP_GateDenies: a tool that is VISIBLE (a wildcard rule matches)
// but DENIED by a later deny rule is rejected at call time by the injected Gate's
// grants.CheckAction layer, before the mutation runs.
func TestNetworkRulesMCP_GateDenies(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	// callerSub="" isolates the CheckAction layer (skips db.Authorize).
	sock := serveNetworkMCP(t, db, "hq", "", []string{"*", "!network_allow"})

	if !listToolNames(t, sock)["network_allow"] {
		t.Fatal("network_allow should be visible (a wildcard rule matches it)")
	}
	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "example.com"}); e == "" {
		t.Fatal("network_allow should be denied by the tier gate")
	}
	if got, _ := db.ListNetworkRules("hq"); len(got) != 0 {
		t.Fatalf("denied allow still wrote a rule: %+v", got)
	}
}

// TestNetworkRulesMCP_AuditRowLands: an agent mutation writes one audit_log row in
// routd.db via resreg's tx-bound EmitInTx (was emitSys/LogIPCAudit). The action is
// network_rules:allow, resource network_rules, surface mcp.
func TestNetworkRulesMCP_AuditRowLands(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_ = db.PutGroup(core.Group{Folder: "hq"})
	grantMCPTools(t, db, "hq", "network_allow", "network_deny", "network_list")
	sock := serveNetworkMCP(t, db, "hq", "folder:hq", deriveFolderGrants(db, "hq"))

	if _, e := callToolText(t, sock, "network_allow", map[string]any{"host": "example.com"}); e != "" {
		t.Fatalf("network_allow errored: %s", e)
	}
	var n int
	err = db.SQL().QueryRow(
		`SELECT count(*) FROM audit_log WHERE action='network_rules:allow' AND outcome='ok' AND folder='hq' AND surface='mcp'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	if n != 1 {
		t.Fatalf("audit_log has %d network_rules:allow rows, want 1", n)
	}
}

// TestValidHostname: moved from ipc when the network tools migrated to resreg
// (network_allow was ipc.validHostname's only caller).
func TestValidHostname(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"example.com", true},
		{"localhost", true},
		{"my-host.example.org", true},
		{"host:8080", true},
		{"", false},
		{strings.Repeat("a", 254), false},
		{"has space", false},
		{"has/slash", false},
		{"has@at", false},
	}
	for _, c := range cases {
		if got := validHostname(c.in); got != c.want {
			t.Errorf("validHostname(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func sliceHasStr(xs []any, want string) bool {
	for _, x := range xs {
		if s, _ := x.(string); s == want {
			return true
		}
	}
	return false
}
