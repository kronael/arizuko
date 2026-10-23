package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// A narrow action named at scope "**" must stay a narrow row. grantACLTx used
// to branch on scope alone, so add_acl(action="mcp:fetch_history", scope="**") discarded the
// action and wrote full role:operator membership instead — and a caller holding
// only (admin, **, grant_option=1) could pass auth.Delegate for an admin grant
// and then mint a strictly stronger right than it held.
func TestGrantACLTx_NarrowActionAtTreeScopeIsNotOperator(t *testing.T) {
	for _, action := range []string{"mcp:fetch_history", "admin", "mcp:reply", "egress"} {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		if err := grantACL(db, core.ACLRow{Principal: "user:a", Scope: "**", Action: action, GrantedBy: "test"}); err != nil {
			t.Fatalf("grant %s: %v", action, err)
		}

		if hasEdge(db, "user:a", "role:operator") {
			t.Errorf("action=%q at ** wrote operator membership; want a plain acl row", action)
		}

		rows := 0
		for _, r := range aclRows(t, db, "user:a") {
			if r.Action == action && r.Scope == "**" {
				rows++
			}
		}
		if rows != 1 {
			t.Errorf("action=%q at ** wrote %d acl rows, want 1", action, rows)
		}
		db.Close()
	}
}

// The operator-grant shape is an OMITTED action (what /root grant and its REST
// twin send) or an explicit "*". Both must still mint the membership edge —
// this is the path TestACLMCP_RootElevationGrantsOperatorRole exercises.
func TestGrantACLTx_OperatorShapeStillMintsMembership(t *testing.T) {
	for _, action := range []string{"", "*"} {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		if err := grantACL(db, core.ACLRow{Principal: "user:b", Scope: "**", Action: action, GrantedBy: "test"}); err != nil {
			t.Fatalf("grant %q: %v", action, err)
		}

		if !hasEdge(db, "user:b", "role:operator") {
			t.Errorf("action=%q at ** did not mint operator membership", action)
		}
		db.Close()
	}
}

// Revoking a narrow row must not strip a membership edge granted separately.
func TestRevokeACLTx_NarrowRevokeLeavesOperatorMembership(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := grantACL(db, core.ACLRow{Principal: "user:c", Scope: "**", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := grantACL(db, core.ACLRow{Principal: "user:c", Scope: "**", Action: "mcp:fetch_history", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	removed, err := revokeACL(db, core.ACLRow{Principal: "user:c", Scope: "**", Action: "mcp:fetch_history"})
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("revoke removed nothing, want the narrow row: a revoke that removes nothing must not read as success")
	}

	if !hasEdge(db, "user:c", "role:operator") {
		t.Error("revoking the narrow ** row stripped the operator membership edge")
	}
}

// TestRevokeACLTx_ReportsZeroWhenNothingMatched: a revoke that removes no row
// must say so. `params` is matched EXACTLY, so a request naming no narrowing
// does not reach a narrowed row — it used to delete nothing and answer
// {"ok":true}, reporting a live grant as revoked (BUGS L79).
func TestRevokeACLTx_ReportsZeroWhenNothingMatched(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := addACLRow(db, core.ACLRow{
		Principal: "user:n", Action: "mcp:fetch_history", Scope: "acme", Effect: "allow",
		Params: "jid=telegram:*", GrantedBy: "test",
	}); err != nil {
		t.Fatal(err)
	}

	removed, err := revokeACL(db, core.ACLRow{Principal: "user:n", Scope: "acme", Action: "mcp:fetch_history", Effect: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Fatal("revoke reported a row removed; the request named no narrowing")
	}

	if live := aclRows(t, db, "user:n"); len(live) != 1 {
		t.Errorf("the narrowed grant count = %d, want 1 (still live)", len(live))
	}
}

// The other half of L79: the narrowed row must be REACHABLE by naming its exact
// narrowing. A writer that can create a row it cannot remove is the same hole
// the empty-literal `params` had, pointing the other way — and it is the
// revocation spec 6/26 § Decision 5 names as the way to undo an `always`.
func TestRevokeACLTx_ExactParamsRemovesTheNarrowedRow(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, params := range []string{"jid=telegram:*", ""} {
		if err := addACLRow(db, core.ACLRow{
			Principal: "user:n", Action: "mcp:fetch_history", Scope: "acme", Effect: "allow",
			Params: params, GrantedBy: "test",
		}); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := revokeACL(db, core.ACLRow{
		Principal: "user:n", Scope: "acme", Action: "mcp:fetch_history", Effect: "allow",
		Params: "jid=telegram:*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("revoke removed nothing: the exact narrowing must address its row")
	}
	// The un-narrowed twin survives: an exact match must not take the neighbour
	// with it, or revoking one verdict would revoke a standing policy row.
	live := aclRows(t, db, "user:n")
	if len(live) != 1 || live[0].Params != "" {
		t.Errorf("the surviving grants are %+v, want only the un-narrowed twin", live)
	}
}
