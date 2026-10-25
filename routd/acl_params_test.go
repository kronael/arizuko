package routd

// Spec 6/26 gap 2.2: the acl writer carries `params`. Until 2026-08-30
// grantACLTx INSERTed it as an empty literal, so no shaped rule was expressible
// on the agent/REST face at all — including both rows Decision 5's standing
// verdicts write. These tests drive the REAL socket, so the Gate's delegation
// check and the handler's write are exercised together: the row auth.Delegate
// judges must be the row that lands.

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// aclRowParams returns the params of the single non-seed row at scope.
func aclRowParams(t *testing.T, db *DB, scope, action string) (string, bool) {
	t.Helper()
	rows, err := db.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Scope == scope && r.Action == action {
			return r.Params, true
		}
	}
	return "", false
}

// grantWithOption gives principal an allow row WITH the grant option, so
// auth.Delegate has something to find a subset of.
func grantWithOption(t *testing.T, db *DB, principal, action, scope, params string) {
	t.Helper()
	if err := addACLRow(db, core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: "allow",
		Params: params, GrantedBy: "test", GrantOption: true,
	}); err != nil {
		t.Fatal(err)
	}
}

// add_acl must WRITE the narrowing it was given. The row is worthless
// otherwise: an operator who asked for `mcp:t(repo=mine/*)` and got `mcp:t`
// granted the tool over every repository.
func TestACLMCP_AddWritesParams(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl", "remove_acl")
	grantWithOption(t, db, "folder:root", "mcp:fetch_history", "root/**", "")
	sock := serveACLMCP(t, db, "root", "folder:root")

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team",
		"action": "mcp:fetch_history", "params": "repo=mine/*",
	}); e != "" {
		t.Fatalf("add_acl: %s", e)
	}
	got, ok := aclRowParams(t, db, "root/team", "mcp:fetch_history")
	if !ok {
		t.Fatal("add_acl wrote no row")
	}
	if got != "repo=mine/*" {
		t.Fatalf("stored params = %q, want %q", got, "repo=mine/*")
	}

	// And the row it wrote is addressable: remove_acl naming the same narrowing
	// drops it. A writer that cannot un-write its own row is BUGS L79 again.
	if _, e := callToolOverSock(t, sock, "remove_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team",
		"action": "mcp:fetch_history", "params": "repo=mine/*",
	}); e != "" {
		t.Fatalf("remove_acl: %s", e)
	}
	if _, ok := aclRowParams(t, db, "root/team", "mcp:fetch_history"); ok {
		t.Fatal("remove_acl naming the exact narrowing did not drop the row")
	}
}

// The spec's own acceptance case (6/26 gap 2.2): a caller who may grant
// `mcp:t(repo=mine/*)` cannot write the unnarrowed `mcp:t`. auth.paramsCover
// reads an empty held spec as "no constraint", so the check only holds if the
// candidate row the Gate builds carries the caller's params unshaved — the
// same drift BUGS L93 had on `effect`.
func TestACLMCP_DelegateJudgesTheShapedRow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"world", "world/a", "world/b"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	// The add_acl tool over the whole `world` subtree, so the FIRST gate passes
	// and the refusal below can only come from auth.Delegate.
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:world/a", Action: "mcp:add_acl", Scope: "world/**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	// Held WITH the grant option, but NARROWED: only calls naming mine/*.
	grantWithOption(t, db, "folder:world/a", "mcp:fetch_history", "world/**", "repo=mine/*")
	sock := serveACLMCP(t, db, "world/a", "folder:world/a")

	// The identical narrowing is a subset of what is held: permitted.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:x", "scope": "world/b",
		"action": "mcp:fetch_history", "params": "repo=mine/*",
	}); e != "" {
		t.Fatalf("the delegable narrowed row was refused: %s", e)
	}
	// The UNNARROWED row is strictly wider than what is held: refused.
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:y", "scope": "world/b", "action": "mcp:fetch_history",
	}); e == "" {
		t.Fatal("a caller holding only mcp:fetch_history(repo=mine/*) wrote the unnarrowed grant")
	}
	if p, ok := aclRowParams(t, db, "world/b", "mcp:fetch_history"); !ok || p != "repo=mine/*" {
		t.Fatalf("world/b holds params %q (present=%v); the refused call must have written nothing", p, ok)
	}
}

// A narrowed row at scope "**" is an ordinary wildcard-scope row, never the
// operator-role overload. Reading it as membership would promote a rule that
// constrains a few arguments into a role that constrains none — the shape of
// the action-discarding bug aclOperatorShape already guards.
func TestGrantACLTx_NarrowedTreeScopeIsNotOperator(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := grantACL(db, core.ACLRow{
		Principal: "user:p", Scope: "**", Params: "jid=telegram:*", GrantedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if hasEdge(db, "user:p", "role:operator") {
		t.Error("a narrowed ** row minted operator membership")
	}
	rows := aclRows(t, db, "user:p")
	if len(rows) != 1 || rows[0].Scope != "**" {
		t.Fatalf("the narrowed ** row was not written as an ordinary acl row: %+v", rows)
	}
	if rows[0].Params != "jid=telegram:*" {
		t.Errorf("stored params = %q, want the narrowing", rows[0].Params)
	}
}

// A narrowing that the evaluator would read differently from how it is stored
// is refused at the door. paramsMatch trims and splits leniently, which is
// right for evaluation and wrong for a writer: the stored string is also the
// revoke key, and a duplicated name ANDs into a row that can never match.
func TestACLMCP_AddRefusesUnreadableParams(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl")
	grantWithOption(t, db, "folder:root", "mcp:fetch_history", "root/**", "")
	sock := serveACLMCP(t, db, "root", "folder:root")

	for _, params := range []string{"repo = mine", "repo=a,repo=b", "=mine", "repo=a,,"} {
		if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
			"principal": "google:bob", "scope": "root/team",
			"action": "mcp:fetch_history", "params": params,
		}); e == "" {
			t.Errorf("add_acl accepted params %q, which the evaluator reads differently", params)
		}
	}
	if _, ok := aclRowParams(t, db, "root/team", "mcp:fetch_history"); ok {
		t.Fatal("a refused add_acl still wrote a row")
	}
}
