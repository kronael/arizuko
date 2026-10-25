package routd

// grant_option is 5/33-paths-roles's WITH GRANT OPTION. grantACLTx omitted the column from
// its INSERT, so both live grant surfaces (add_acl and POST /v1/acl) left it at
// the DEFAULT and re-delegation was reachable only through resreg.Apply. These
// tests drive the real socket, so the Gate's auth.Delegate check and the
// handler's write are exercised together.

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// aclRowGrantOption returns the grant option of the single row at scope+action.
func aclRowGrantOption(t *testing.T, db *DB, scope, action string) (bool, bool) {
	t.Helper()
	rows, err := db.ListACL("")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Scope == scope && r.Action == action {
			return r.GrantOption, true
		}
	}
	return false, false
}

// add_acl must WRITE the grant option it was given, and must NOT invent one it
// was not asked for.
func TestACLMCP_AddWritesGrantOption(t *testing.T) {
	for _, c := range []struct {
		name string
		args map[string]any
		want bool
	}{
		{"asked for", map[string]any{"grant_option": true}, true},
		{"omitted", map[string]any{}, false},
		{"explicitly false", map[string]any{"grant_option": false}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
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

			args := map[string]any{
				"principal": "google:bob", "scope": "root/team", "action": "mcp:fetch_history",
			}
			for k, v := range c.args {
				args[k] = v
			}
			if _, e := callToolOverSock(t, sock, "add_acl", args); e != "" {
				t.Fatalf("add_acl: %s", e)
			}

			got, ok := aclRowGrantOption(t, db, "root/team", "mcp:fetch_history")
			if !ok {
				t.Fatal("add_acl wrote no row")
			}
			if got != c.want {
				t.Fatalf("stored grant_option = %v, want %v", got, c.want)
			}
		})
	}
}

// The point of the option: a row granted WITH it can be re-granted by its
// holder. Without the write, the chain stopped at the first hop and only
// resreg.Apply could start one.
func TestACLMCP_GrantedOptionLetsGranteeRegrant(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team", "root/team/sub"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl")
	grantMCPTools(t, db, "root/team", "add_acl")
	grantWithOption(t, db, "folder:root", "mcp:fetch_history", "root/**", "")

	// Hop 1: root delegates `mcp:fetch_history` to root/team WITH the option.
	sock := serveACLMCP(t, db, "root", "folder:root")
	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "folder:root/team", "scope": "root/team/**",
		"action": "mcp:fetch_history", "grant_option": true,
	}); e != "" {
		t.Fatalf("hop 1 add_acl: %s", e)
	}

	// Hop 2: root/team re-grants what it now holds. auth.Delegate's
	// grantableRows only contains grant_option=1 rows, so this hop proves the
	// column actually landed.
	sub := serveACLMCP(t, db, "root/team", "folder:root/team")
	if _, e := callToolOverSock(t, sub, "add_acl", map[string]any{
		"principal": "google:carol", "scope": "root/team/sub", "action": "mcp:fetch_history",
	}); e != "" {
		t.Fatalf("hop 2 add_acl (grantee could not re-grant): %s", e)
	}

	if _, ok := aclRowGrantOption(t, db, "root/team/sub", "mcp:fetch_history"); !ok {
		t.Fatal("hop 2 wrote no row")
	}
}

// A holder WITHOUT the option cannot pass one on: coveredByGrantable only ever
// sees rows the caller holds with grant_option=1, so the whole delegation is
// refused. This is the guard that keeps the new argument from being an
// escalation.
func TestACLMCP_NoOptionCannotDelegate(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl")
	// `mcp:fetch_history` held with NO grant option.
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:root", Action: "mcp:fetch_history", Scope: "root/**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	sock := serveACLMCP(t, db, "root", "folder:root")

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team",
		"action": "mcp:fetch_history", "grant_option": true,
	}); e == "" {
		t.Fatal("a holder without the grant option minted one")
	}
	if _, ok := aclRowGrantOption(t, db, "root/team", "mcp:fetch_history"); ok {
		t.Fatal("the refused delegation still wrote a row")
	}
}
