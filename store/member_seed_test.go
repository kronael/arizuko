package store

import (
	"context"
	"testing"
	"time"

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
	if n := edgesTo(t, s.Obj(), "folder:acme", RoleMember); n != 1 {
		t.Fatalf("acl_membership rows for folder:acme = %d, want 1", n)
	}

	// Idempotent: the upsert path runs the same seed and must not fail or
	// duplicate.
	if err := s.PutGroupRow(core.Group{Folder: "acme", Product: "container"}); err != nil {
		t.Fatalf("second PutGroupRow: %v", err)
	}
	if n := edgesTo(t, s.Obj(), "folder:acme", RoleMember); n != 1 {
		t.Fatalf("after a re-put, rows = %d, want 1", n)
	}
}

// SeedMemberRole is what dashd's and onbod's folder creations call, and it
// carries THEIR writes: the groups row a member edge implies (BUGS L119) lands
// in the same guarded batch as the edge, so neither can exist without the other.
//
// It is idempotent, and the second run must not refuse: the edge is
// create-if-absent and the caller's own write is what a false would be about.
func TestSeedMemberRoleCarriesTheCreationItBelongsTo(t *testing.T) {
	s := openEntities(t)
	ctx := context.Background()
	row, err := GroupCreateWrite(core.Group{Folder: "world", AddedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	created, err := SeedMemberRole(ctx, s, "world", row)
	if err != nil || !created {
		t.Fatalf("the first creation reported created=%v err=%v", created, err)
	}
	if n := edgesTo(t, s.Obj(), "folder:world", RoleMember); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	if _, ok := s.GroupByFolder("world"); !ok {
		t.Fatal("the groups row did not land with the edge it implies")
	}

	// A second creation loses: the groups write is the only guarded one in the
	// batch, so a false names exactly the folder already being someone's.
	again, err := SeedMemberRole(ctx, s, "world", row)
	if err != nil {
		t.Fatalf("the second creation errored: %v", err)
	}
	if again {
		t.Error("a second creation of the same folder reported success; the caller would be " +
			"granted admin on somebody else's world")
	}
	if n := edgesTo(t, s.Obj(), "folder:world", RoleMember); n != 1 {
		t.Errorf("rows = %d after a refused creation, want 1", n)
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
