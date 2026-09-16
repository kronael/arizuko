package routd

// BUGS L272: the acl row's `effect` decides whether the row grants or refuses,
// and auth.Authorize reads every value that is not exactly `deny` as ALLOW. A
// face that writes the string it was handed therefore turns a typo — or a
// deliberate near-miss — into a grant. The check belongs where the row is
// WRITTEN, so no face can be the one that forgot it.

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// aclDelegator stands up a folder that may delegate `read` over its own subtree
// — the precondition add_acl needs before the effect is even looked at.
func aclDelegator(t *testing.T) (*DB, string) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, f := range []string{"root", "root/team"} {
		_ = db.PutGroup(core.Group{Folder: f})
	}
	grantMCPTools(t, db, "root", "add_acl")
	if err := addACLRow(db, core.ACLRow{
		Principal: "folder:root", Action: "read", Scope: "root/**", Effect: "allow",
		GrantedBy: "test", GrantOption: true,
	}); err != nil {
		t.Fatal(err)
	}
	return db, serveACLMCP(t, db, "root", "folder:root")
}

// TestACLMCP_JunkEffectIsRefused: `denny` is not a spelling of deny, and the
// evaluator reads it as allow. The agent face must refuse it rather than write
// a grant the operator would read as a refusal.
func TestACLMCP_JunkEffectIsRefused(t *testing.T) {
	db, sock := aclDelegator(t)

	_, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team",
		"action": "read", "effect": "denny",
	})
	if e == "" {
		t.Fatal("add_acl accepted an effect no evaluator understands")
	}
	if n := aclRowCount(t, db, "root/team"); n != 0 {
		t.Fatalf("a refused add still wrote %d row(s)", n)
	}
	// The escalation the refusal prevents: a row nobody validated grants the
	// action, because every effect that is not exactly `deny` is an allow.
	if auth.Authorize(store.NewOver(db.Obj()), auth.Caller{Principal: "google:bob"},
		"read", "root/team", nil) {
		t.Fatal("the junk effect became a grant")
	}
}

// TestACLMCP_DenyAndAllowStillWrite: the guard refuses only what no evaluator
// reads. Both real spellings still go through this face.
func TestACLMCP_DenyAndAllowStillWrite(t *testing.T) {
	db, sock := aclDelegator(t)

	if _, e := callToolOverSock(t, sock, "add_acl", map[string]any{
		"principal": "google:bob", "scope": "root/team",
		"action": "read", "effect": "allow",
	}); e != "" {
		t.Fatalf("add_acl allow: %s", e)
	}
	if n := aclRowCount(t, db, "root/team"); n != 1 {
		t.Fatalf("allow wrote %d rows, want 1", n)
	}
}

// TestStoreACLWriter_RefusesJunkEffect: the guard is at the WRITER, so a caller
// that never passes through a face cannot bypass it either.
func TestStoreACLWriter_RefusesJunkEffect(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s := store.NewOver(db.Obj())
	if err := s.PutACLRow(core.ACLRow{
		Principal: "google:bob", Action: "read", Scope: "root", Effect: "denny",
	}); err == nil {
		t.Fatal("the store writer accepted an effect no evaluator understands")
	}
}
