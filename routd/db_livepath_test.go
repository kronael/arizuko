package routd

import (
	"context"
	"errors"
	"fmt"
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
	return store.NewOver(d.Obj()).AddACLRow(row)
}

func addMembership(d *DB, child, parent, addedBy string) error {
	return store.NewOver(d.Obj()).PutMembership(child, parent, addedBy)
}

func setThreadReplies(d *DB, folder string, on *bool) error {
	return store.NewOver(d.Obj()).SetThreadReplies(folder, on)
}

func messageExists(d *DB, chatJID, id string) bool {
	_, ok := store.NewOver(d.Obj()).MessageByID(chatJID, id)
	return ok
}

// msgByID reads one message back through the entity. A message is addressed by
// its chat and its position, so a fixture that wants one by id names the chat
// it is in — an unscoped lookup would resolve a platform id in another tenant's
// partition.
func msgByID(t *testing.T, d *DB, chatJID, id string) core.Message {
	t.Helper()
	m, ok := store.NewOver(d.Obj()).MessageByID(chatJID, id)
	if !ok {
		t.Fatalf("no message %s in %s", id, chatJID)
	}
	return m
}

// chatMessages reads a chat's whole history, oldest first.
func chatMessages(t *testing.T, d *DB, chatJID string) []core.Message {
	t.Helper()
	msgs, err := d.MessagesSince(chatJID, "")
	if err != nil {
		t.Fatal(err)
	}
	return msgs
}

// turnBotRow reads the one bot row a turn stamped in a chat.
func turnBotRow(t *testing.T, d *DB, chatJID, turnID string) core.Message {
	t.Helper()
	frames, err := store.NewOver(d.Obj()).TurnFrames(chatJID, turnID, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 1 {
		t.Fatalf("turn %s stamped %d bot rows in %s, want 1", turnID, len(frames), chatJID)
	}
	return frames[0]
}

func pendingActionRow(d *DB, id string) (PendingAction, bool) {
	return d.pendingAction(id)
}

// resolvePending records a verdict the way both production faces do: onto a
// write set the caller owns, so the conditional write that serializes two
// verdicts rides whatever else the caller is writing.
func resolvePending(d *DB, id, status, reviewer, note string) (PendingAction, error) {
	ws := &store.WriteSet{}
	p, err := d.resolvePendingActionInto(ws, id, status, reviewer, note, errVerdictLost(id))
	if err != nil {
		return PendingAction{}, err
	}
	return p, d.store().Apply(context.Background(), ws)
}

// errVerdictLost is what a refused verdict means, which production declares at
// its own surface — a 409 on REST, a chat line in steer.
func errVerdictLost(id string) error {
	return fmt.Errorf("pending action %q %w (by someone else)", id, ErrPendingResolved)
}

// resolveHold runs the shared funnel and sends its batch, the way both faces do.
func resolveHold(d *DB, id, verdict, reviewer, note string, standing bool) (PendingAction, error) {
	ws := &store.WriteSet{}
	p, err := d.resolveHoldInto(context.Background(), ws, id, verdict, reviewer, note,
		standing, errVerdictLost(id))
	if err != nil {
		return PendingAction{}, err
	}
	return p, d.store().Apply(context.Background(), ws)
}

// grantACL sends one grant as its own batch — production buffers it beside the
// audit row resreg records.
func grantACL(d *DB, row core.ACLRow) error {
	ws := &store.WriteSet{}
	if err := d.grantACLInto(context.Background(), ws, row); err != nil {
		return err
	}
	return d.store().Apply(context.Background(), ws)
}

// revokeACL sends one revoke as its own batch and reports whether a row went.
// The write conditions on the row being there, so the REFUSED batch is the
// not-found answer — which is how production reads it too.
func revokeACL(d *DB, row core.ACLRow) (bool, error) {
	gone := errors.New("no matching grant")
	ws := &store.WriteSet{}
	if err := d.revokeACLInto(ws, row, gone); err != nil {
		return false, err
	}
	switch err := d.store().Apply(context.Background(), ws); {
	case errors.Is(err, gone):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}

// aclRows returns the grants held by one principal, through the reader the
// evaluator itself uses. A grant is an item keyed by its whole six-field
// coordinate, so counting rows means reading them.
func aclRows(t *testing.T, d *DB, principal string) []core.ACLRow {
	t.Helper()
	rows, err := store.NewOver(d.Obj()).ListACL(principal)
	if err != nil {
		t.Fatalf("read %s's grants: %v", principal, err)
	}
	return rows
}

// hasEdge reports whether the DIRECT edge (child → parent) is written. Members
// answers the one direction the projection cares about; Ancestors would answer
// about the transitive closure, which is a different question.
func hasEdge(d *DB, child, parent string) bool {
	return slices.Contains(store.NewOver(d.Obj()).Members(parent), child)
}
