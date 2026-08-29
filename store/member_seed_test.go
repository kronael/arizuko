package store

import (
	"testing"

	"github.com/kronael/arizuko/core"
)

// The invariant is "a groups row implies the member edge". It used to live in
// exactly one of four creating writers (routd's PutGroup), so a group made by
// the CLI, by dashd or by web onboarding expanded to nothing, matched zero
// allow rows, and came up with no messaging tools at all (BUGS L119).
func TestPutGroupRowSeedsTheMemberEdge(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()

	if err := s.PutGroupRow(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
		"folder:acme", RoleMember).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("acl_membership rows for folder:acme = %d, want 1", n)
	}

	// Idempotent: the upsert path runs the same seed and must not fail or
	// duplicate.
	if err := s.PutGroupRow(core.Group{Folder: "acme", Product: "container"}); err != nil {
		t.Fatalf("second PutGroupRow: %v", err)
	}
	if err := s.DB().QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
		"folder:acme", RoleMember).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("after a re-put, rows = %d, want 1", n)
	}
}

// SeedMemberRoleTx is what dashd's and onbod's own transactions call, so it has
// to work on a caller-owned tx and be idempotent there too.
func TestSeedMemberRoleTxIsIdempotentOnACallerTx(t *testing.T) {
	s, _ := OpenMem()
	defer s.Close()
	tx, err := s.DB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for range 2 {
		if err := SeedMemberRoleTx(tx, "world"); err != nil {
			t.Fatalf("SeedMemberRoleTx: %v", err)
		}
	}
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM acl_membership WHERE child=? AND parent=?`,
		"folder:world", RoleMember).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}
