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
// is the schema these rows live on, and it is routd migration 0047 that makes
// `added_by` NOT NULL.

import (
	"context"
	"errors"
	"testing"

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
	got, err := r.ScanAll(resreg.OnDB(db.SQL()))
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

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := addMembershipTx(context.Background(), tx, "user:alice", "role:operator", ""); err != nil {
		t.Fatalf("addMembershipTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
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

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := addMembershipTx(context.Background(), tx,
		"telegram:user/42", "google:alice", store.PairingAddedBy); err != nil {
		t.Fatalf("addMembershipTx: %v", err)
	}
	if err := addMembershipTx(context.Background(), tx,
		"user:alice", "role:operator", "cli:kron"); err != nil {
		t.Fatalf("addMembershipTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	rows := membershipRows(t, db)
	if len(rows) != 1 || rows[0].Child != "user:alice" {
		t.Fatalf("projection = %+v, want only the role edge; a pairing edge in the "+
			"manifest is authority a rollback would resurrect", rows)
	}
}

// The column carries no NULL, so the filter has a value to compare on every row.
func TestMembership_AddedByIsNotNullable(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.SQL().Exec(
		`INSERT INTO acl_membership (child, parent, added_by, added_at) VALUES (?,?,NULL,?)`,
		"a", "b", "2026-01-01T00:00:00Z"); err == nil {
		t.Error("acl_membership accepted a NULL added_by; the row filter cannot compare " +
			"one, so the edge would be invisible to every export and every apply")
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

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := addMembershipTx(ctx, tx, "a", "b", "cli"); err != nil {
		t.Fatalf("a -> b: %v", err)
	}
	if err := addMembershipTx(ctx, tx, "b", "c", "cli"); err != nil {
		t.Fatalf("b -> c: %v", err)
	}
	if err := addMembershipTx(ctx, tx, "c", "a", "cli"); !errors.Is(err, store.ErrCycle) {
		t.Errorf("c -> a returned %v, want store.ErrCycle: a is already two edges above c, "+
			"and an edge that closes the loop hands every member of it the union of the "+
			"whole ring's authority", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
}
