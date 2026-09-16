package resreg

// rollforward_test.go is BUGS L267 as a body: an apply that dies after writing
// keeps its subsystem's claim, and the operator's escape has to be to run the
// apply again rather than to edit `resreg_scopes` by hand.
//
// THE FAILURE IS THE ONE THAT WAS REPRODUCED, not a stand-in. L267 was produced
// by running `arizuko apply` before `arizuko migrate-audit`: every row write
// lands, and the apply's LAST write — its single audit row — hits a table that
// is not there. So the fixture here leaves the audit entity uncreated and lets
// the real write fail, the same way. Nothing stubs Apply and nothing reaches
// into the marker to wedge it.

import (
	"errors"
	"reflect"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
)

// halfMigrated is freshEntity with the audit entity DELIBERATELY absent: the
// state a database is in between `migrate-routd` and `migrate-audit`, and the
// state L267 was reproduced from.
func halfMigrated(t *testing.T) (Backend, *Resource, *dynstore.Store) {
	t.Helper()
	reset()
	s := dyntest.Store(t)
	r := Register(Resource{
		Name:          "entityrows",
		Table:         "entityrows",
		DB:            entitySubsystem,
		RowType:       reflect.TypeFor[EntityRow](),
		PKFields:      []string{"Key"},
		EntitySortKey: []string{"Key"},
	})
	if err := EnsureSubsystem(t.Context(), s, s, entitySubsystem); err != nil {
		t.Fatalf("EnsureSubsystem: %v", err)
	}
	return On(s), r, s
}

// markerAttr reads one attribute of a subsystem's version marker straight
// through the driver, so an assertion about the claim cannot agree with a bug in
// the protocol that sets it.
func markerAttr(t *testing.T, be Backend, attr string) string {
	t.Helper()
	sc := store.OwnerScope(entitySubsystem)
	items, err := be.Obj.Query(t.Context(), objstore.Query{
		Entity: sc.Entity, Partition: sc.Key.Partition,
		Key: objstore.KeyPredicate{Op: objstore.ExactKey, Value: sc.Key.Sort},
	})
	if err != nil {
		t.Fatalf("read the marker: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("the subsystem carries %d markers, want 1", len(items))
	}
	return items[0].Attrs[attr]
}

// TestEntity_APartAppliedScopeIsRolledForwardByTheSameManifest is L267.
//
// The claim staying is CORRECT and this body asserts it: a released one would
// make a part-written scope read as settled, which is the silent corruption the
// claim exists to stop. What was missing is the other half — the operator fixes
// the cause, runs the SAME apply again, and it has to finish the work rather
// than answer ErrScopeHeld forever.
//
// The roll-forward is sound because the apply is idempotent over its OWN partial
// results: the delete set is derived from the manifest's scopes and re-read
// live, so deleting whatever is there now and putting the document's rows over
// it lands on exactly the state a clean run would have produced.
func TestEntity_APartAppliedScopeIsRolledForwardByTheSameManifest(t *testing.T) {
	be, r, s := halfMigrated(t)
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	rows := []EntityRow{{Key: "a", Note: "one", Count: 1}, {Key: "b", Note: "two", Count: 2}}
	manifest := map[string]any{"entityrows": rows}
	opts := func() *ApplyOpts { return &ApplyOpts{Actor: "test", ManifestDigest: "d"} }

	// The apply that dies after writing. Its rows land; its audit row does not.
	if _, err := Apply(t.Context(), be, entitySubsystem, sum, false, manifest, opts()); err == nil {
		t.Fatal("the apply succeeded with no audit entity to write into, so the fixture " +
			"never reproduced the failure this body is about")
	}
	if h := markerAttr(t, be, "holder"); h == "" {
		t.Fatal("the failed apply gave its claim back, so a scope left part-written now " +
			"reads as settled and correct")
	}

	// The operator fixes the cause — this is `arizuko migrate-audit` — and runs
	// the SAME apply again. That is the roll-forward, and it is the whole bug.
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("create the audit entity: %v", err)
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, sum, false, manifest, opts()); err != nil {
		t.Fatalf("the same manifest could not roll the part-applied scope forward: %v\n"+
			"one failed apply then disables the config lane for this subsystem forever, "+
			"and the only escape is editing the marker by hand", err)
	}
	if h := markerAttr(t, be, "holder"); h != "" {
		t.Errorf("the roll-forward left the claim %q on the scope", h)
	}
	if got := scanRows[EntityRow](t, be, r); !reflect.DeepEqual(got, rows) {
		t.Errorf("rows = %v, want the document's %v", got, rows)
	}

	// And the recovered scope is a FIXED POINT: the same manifest applied to the
	// state it just produced changes nothing.
	settled, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	next, err := Apply(t.Context(), be, entitySubsystem, settled, false, manifest, opts())
	if err != nil {
		t.Fatalf("the apply after the roll-forward: %v", err)
	}
	if next != settled {
		t.Errorf("a second apply of the same manifest moved the checksum %s -> %s", settled, next)
	}
}

// A DIFFERENT manifest may NOT finish somebody else's replace.
//
// This is the safety half, and it is the one a wrong fix turns red: an apply
// whose document does not cover the rows the dead one was writing would settle a
// scope that is still part-written outside its own delete set. The claim comes
// back only to work that can prove it writes the same rows.
func TestEntity_APartAppliedScopeRefusesADifferentManifest(t *testing.T) {
	be, r, s := halfMigrated(t)
	sum, err := Checksum(be, entitySubsystem)
	if err != nil {
		t.Fatalf("Checksum: %v", err)
	}
	if _, err := Apply(t.Context(), be, entitySubsystem, sum, false, map[string]any{
		"entityrows": []EntityRow{{Key: "a", Count: 1}},
	}, &ApplyOpts{Actor: "test"}); err == nil {
		t.Fatal("the apply succeeded with no audit entity, so nothing is part-written")
	}
	if err := s.CreateEntity(t.Context(), "audit_log"); err != nil {
		t.Fatalf("create the audit entity: %v", err)
	}
	held := scanRows[EntityRow](t, be, r)

	_, err = Apply(t.Context(), be, entitySubsystem, "", true, map[string]any{
		"entityrows": []EntityRow{{Key: "z", Count: 9}},
	}, &ApplyOpts{Actor: "test"})
	if !errors.Is(err, objstore.ErrScopeHeld) {
		t.Fatalf("an apply of a DIFFERENT manifest over a part-applied scope returned %v, "+
			"want ErrScopeHeld: it would have declared settled a scope still part-written "+
			"outside its own delete set", err)
	}
	if h := markerAttr(t, be, "holder"); h == "" {
		t.Error("the refused apply cleared the claim, so the part-written scope now reads " +
			"as settled")
	}
	if got := scanRows[EntityRow](t, be, r); !reflect.DeepEqual(got, held) {
		t.Errorf("rows = %v, want the part-written %v — a refused apply writes nothing", got, held)
	}
}
