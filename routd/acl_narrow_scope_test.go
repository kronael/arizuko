package routd

import "testing"

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
		if err := grantACLTx(t.Context(), tx, "user:a", "**", action, "", "test"); err != nil {
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
		if err := grantACLTx(t.Context(), tx, "user:b", "**", action, "", "test"); err != nil {
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
	if err := grantACLTx(t.Context(), tx, "user:c", "**", "", "", "test"); err != nil {
		t.Fatal(err)
	}
	if err := grantACLTx(t.Context(), tx, "user:c", "**", "read", "", "test"); err != nil {
		t.Fatal(err)
	}
	if err := revokeACLTx(t.Context(), tx, "user:c", "**", "read", ""); err != nil {
		t.Fatal(err)
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
