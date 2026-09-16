package routd

// The membership row filter is one declaration the export, the wholesale delete
// and the scoped delete all honour, and on the object path a filter is a
// conjunction of attribute comparisons with no OR in it. So the predicate is
// `added_by <> 'pairing'`, and the value it compares has to BE there: NULL is
// the one thing it cannot answer about, and a row it cannot answer about falls
// out of its own resource's projection — invisible to export, and untouched by
// the apply that should have replaced it.
//
// It runs HERE for the reason TestResourceRowTypesCoverTheirTable does: routd.db
// is the schema these rows live on, and routd's agent-facing grant is one of the
// writers that has to store the value the filter compares.

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// membershipRows reads the resource's manifest-visible projection.
func membershipRows(t *testing.T, db *DB) []resources.ACLMembershipRow {
	t.Helper()
	r := resreg.Lookup(resources.ACLMembershipName)
	if r == nil {
		t.Fatal("acl_membership is not registered")
	}
	got, err := r.ScanAll(resreg.On(db.Obj()))
	if err != nil {
		t.Fatalf("ScanAll: %v", err)
	}
	return got.([]resources.ACLMembershipRow)
}

// An edge whose caller named no actor is still declarative config, so it stays
// in the projection. It only does because the writer stores the empty string
// rather than NULL.
func TestMembership_UnattributedEdgeStaysInTheProjection(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := putEdge(t, db, "user:alice", "role:operator", ""); err != nil {
		t.Fatalf("put edge: %v", err)
	}

	rows := membershipRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("the projection holds %d rows, want the one edge that was written; "+
			"an unattributed edge the filter cannot compare is a membership row no "+
			"export shows and no apply replaces", len(rows))
	}
	if rows[0].AddedBy != "" {
		t.Errorf("added_by = %q, want the empty string the caller named", rows[0].AddedBy)
	}
}

// The other half of the same predicate, and the reason it exists: a pairing edge
// is consented runtime state and never enters the projection (spec 5/31).
func TestMembership_PairingEdgeStaysOutOfTheProjection(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := putEdge(t, db, "telegram:user/42", "google:alice", store.PairingAddedBy); err != nil {
		t.Fatalf("put pairing edge: %v", err)
	}
	if err := putEdge(t, db, "user:alice", "role:operator", "cli:kron"); err != nil {
		t.Fatalf("put role edge: %v", err)
	}

	rows := membershipRows(t, db)
	if len(rows) != 1 || rows[0].Child != "user:alice" {
		t.Fatalf("projection = %+v, want only the role edge; a pairing edge in the "+
			"manifest is authority a rollback would resurrect", rows)
	}
}

// `added_by` is written even when it is EMPTY, so the filter has a value to
// compare on every edge. Absent is the only null this model has, and an item
// that left the attribute off would answer no comparison at all.
func TestMembership_AddedByIsAlwaysWritten(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := putEdge(t, db, "a", "b", ""); err != nil {
		t.Fatalf("put edge: %v", err)
	}
	items, err := db.Obj().Query(context.Background(), objstore.Query{
		Entity: store.ACLMembership, Partition: store.OwnerRoutd,
	})
	if err != nil {
		t.Fatalf("read edges: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("the partition holds %d edges, want the one that was written", len(items))
	}
	if _, ok := items[0].Attrs["added_by"]; !ok {
		t.Error("the edge carries no added_by attribute; the row filter cannot compare " +
			"one that is not there, so the edge would be invisible to every export and " +
			"every apply")
	}
}

// The cycle guard is one walk with four callers (objstore.Reachable), and this
// is routd's — the agent-facing add. A transitive cycle is the case a
// single-level check answers wrong, so it is the one worth stating (BUGS L256).
func TestMembership_TransitiveCycleIsRefused(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := putEdge(t, db, "a", "b", "cli"); err != nil {
		t.Fatalf("a -> b: %v", err)
	}
	if err := putEdge(t, db, "b", "c", "cli"); err != nil {
		t.Fatalf("b -> c: %v", err)
	}
	if err := putEdge(t, db, "c", "a", "cli"); !errors.Is(err, store.ErrCycle) {
		t.Errorf("c -> a returned %v, want store.ErrCycle: a is already two edges above c, "+
			"and an edge that closes the loop hands every member of it the union of the "+
			"whole ring's authority", err)
	}
}

// putEdge writes one edge the way routd's agent-facing grant does: through
// store.PutMembershipInto on a batch of its own. One edge per batch is what
// production issues — the walk that refuses a cycle reads landed edges, so a ring
// built inside ONE batch is a shape no caller produces and no walk can see.
func putEdge(t *testing.T, db *DB, child, parent, addedBy string) error {
	t.Helper()
	ctx := context.Background()
	ws := &store.WriteSet{}
	if err := db.store().PutMembershipInto(ctx, ws, child, parent, addedBy); err != nil {
		return err
	}
	return db.store().Apply(ctx, ws)
}
