package store

import (
	"context"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
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
	if n := edgesTo(t, s.reads(nil), "folder:acme", RoleMember); n != 1 {
		t.Fatalf("acl_membership rows for folder:acme = %d, want 1", n)
	}

	// Idempotent: the upsert path runs the same seed and must not fail or
	// duplicate.
	if err := s.PutGroupRow(core.Group{Folder: "acme", Product: "container"}); err != nil {
		t.Fatalf("second PutGroupRow: %v", err)
	}
	if n := edgesTo(t, s.reads(nil), "folder:acme", RoleMember); n != 1 {
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
		if err := SeedMemberRoleTx(context.Background(), s, tx, "world"); err != nil {
			t.Fatalf("SeedMemberRoleTx: %v", err)
		}
	}
	// Read through the caller's own transaction: the edges are not committed
	// yet, so the pool would not see them.
	if n := edgesTo(t, s.reads(tx), "folder:world", RoleMember); n != 1 {
		t.Errorf("rows = %d, want 1", n)
	}
}

// edgesTo counts the membership items at one (child, parent) coordinate.
func edgesTo(t *testing.T, q EntityQuerier, child, parent string) int {
	t.Helper()
	key, err := membershipKey(child, parent)
	if err != nil {
		t.Fatal(err)
	}
	items, err := q.Query(context.Background(), objstore.Query{
		Entity: ACLMembership, Partition: OwnerRoutd,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: key.Sort},
	})
	if err != nil {
		t.Fatal(err)
	}
	return len(items)
}
