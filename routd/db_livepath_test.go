package routd

import (
	"context"
	"slices"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
)

// Fixtures reach these tables through store.Store and through the tx seams the
// resources use — the paths production takes. routd.DB's wrappers around them
// had no production caller and are deleted (BUGS L14); these helpers replace
// them without becoming a second implementation of the write.

func addACLRow(d *DB, row core.ACLRow) error {
	return store.New(d.SQL()).AddACLRow(row)
}

func addMembership(d *DB, child, parent, addedBy string) error {
	return store.New(d.SQL()).PutMembership(child, parent, addedBy)
}

func setThreadReplies(d *DB, folder string, on *bool) error {
	return store.New(d.SQL()).SetThreadReplies(folder, on)
}

func messageExists(d *DB, id string) bool {
	_, ok := store.New(d.SQL()).MessageByID(id)
	return ok
}

func pendingActionRow(d *DB, id string) (PendingAction, bool) {
	return d.pendingAction(id)
}

// resolvePending records a verdict the way both production faces do: on a
// transaction the caller owns, so the conditional write that serializes two
// verdicts commits with whatever else the caller is writing.
func resolvePending(d *DB, id, status, reviewer, note string) (PendingAction, error) {
	tx, err := d.SQL().Begin()
	if err != nil {
		return PendingAction{}, err
	}
	defer tx.Rollback() //nolint:errcheck
	p, err := d.resolvePendingActionTx(context.Background(), tx, id, status, reviewer, note)
	if err != nil {
		return PendingAction{}, err
	}
	return p, tx.Commit()
}

// aclRows returns the grants held by one principal, through the reader the
// evaluator itself uses. A grant is an item keyed by its whole six-field
// coordinate, so counting rows means reading them.
func aclRows(t *testing.T, d *DB, principal string) []core.ACLRow {
	t.Helper()
	rows, err := store.New(d.SQL()).ListACL(principal)
	if err != nil {
		t.Fatalf("read %s's grants: %v", principal, err)
	}
	return rows
}

// hasEdge reports whether the DIRECT edge (child → parent) is written. Members
// answers the one direction the projection cares about; Ancestors would answer
// about the transitive closure, which is a different question.
func hasEdge(d *DB, child, parent string) bool {
	return slices.Contains(store.New(d.SQL()).Members(parent), child)
}
