package routd

import (
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
	return pendingActionOn(d.SQL(), id)
}

func resolvePending(d *DB, id, status, reviewer, note string) (PendingAction, error) {
	return resolvePendingActionOn(d.SQL(), id, status, reviewer, note)
}
