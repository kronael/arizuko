package routd

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// A narrow action named at scope "**" must stay a narrow row. grantACLTx used
// to branch on scope alone, so add_acl(action="read", scope="**") discarded the
// action and wrote full role:operator membership instead — and a caller holding
// only (admin, **, grant_option=1) could pass auth.Delegate for an admin grant
// and then mint a strictly stronger right than it held.
func TestGrantACLTx_NarrowActionAtTreeScopeIsNotOperator(t *testing.T) {
	for _, action := range []string{"read", "admin", "mcp:reply", "egress"} {
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.SQL().Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := grantACLTx(t.Context(), tx, core.ACLRow{Principal: "user:a", Scope: "**", Action: action, GrantedBy: "test"}); err != nil {
			t.Fatalf("grant %s: %v", action, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		var members int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
			"user:a", "role:operator").Scan(&members); err != nil {
			t.Fatal(err)
		}
		if members != 0 {
			t.Errorf("action=%q at ** wrote operator membership; want a plain acl row", action)
		}

		var rows int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM acl WHERE principal=? AND action=? AND scope='**'`,
			"user:a", action).Scan(&rows); err != nil {
			t.Fatal(err)
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
		tx, err := db.SQL().Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := grantACLTx(t.Context(), tx, core.ACLRow{Principal: "user:b", Scope: "**", Action: action, GrantedBy: "test"}); err != nil {
			t.Fatalf("grant %q: %v", action, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		var members int
		if err := db.SQL().QueryRow(
			`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
			"user:b", "role:operator").Scan(&members); err != nil {
			t.Fatal(err)
		}
		if members != 1 {
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

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := grantACLTx(t.Context(), tx, core.ACLRow{Principal: "user:c", Scope: "**", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := grantACLTx(t.Context(), tx, core.ACLRow{Principal: "user:c", Scope: "**", Action: "read", GrantedBy: "test"}); err != nil {
		t.Fatal(err)
	}
	n, err := revokeACLTx(t.Context(), tx, core.ACLRow{Principal: "user:c", Scope: "**", Action: "read"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("revoke removed %d rows, want 1: a revoke that removes nothing must not read as success", n)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	var members int
	if err := db.SQL().QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
		"user:c", "role:operator").Scan(&members); err != nil {
		t.Fatal(err)
	}
	if members != 1 {
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
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES ('user:n', 'read', 'acme', 'allow', 'jid=telegram:*', '', 'test', '')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	n, err := revokeACLTx(t.Context(), tx, core.ACLRow{Principal: "user:n", Scope: "acme", Action: "read", Effect: "allow"})
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("revoke reported %d rows removed; the request named no narrowing", n)
	}

	var live int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM acl WHERE principal='user:n'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != 1 {
		t.Errorf("the narrowed grant count = %d, want 1 (still live)", live)
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
	if _, err := db.SQL().Exec(
		`INSERT INTO acl (principal, action, scope, effect, params, predicate, granted_by, granted_at)
		 VALUES ('user:n', 'read', 'acme', 'allow', 'jid=telegram:*', '', 'test', ''),
		        ('user:n', 'read', 'acme', 'allow', '', '', 'test', '')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	n, err := revokeACLTx(t.Context(), tx, core.ACLRow{
		Principal: "user:n", Scope: "acme", Action: "read", Effect: "allow",
		Params: "jid=telegram:*",
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("revoke removed %d rows, want 1: the exact narrowing must address its row", n)
	}
	// The un-narrowed twin survives: an exact match must not take the neighbour
	// with it, or revoking one verdict would revoke a standing policy row.
	var live string
	if err := tx.QueryRow(
		`SELECT params FROM acl WHERE principal='user:n'`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	if live != "" {
		t.Errorf("the surviving row carries params %q, want the un-narrowed twin", live)
	}
}
