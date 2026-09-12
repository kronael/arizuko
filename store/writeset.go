package store

// writeset.go holds the sink a caller hands its writes to instead of performing
// them, so the layer that opened the call sends them together with the row that
// records the call.
//
// It is the shape a HANDLER needs and audit_helpers.go's build func is not: a
// resreg handler runs as a call of its own, decides several writes across
// several store methods, and returns a value to the wire — it cannot be a single
// closure that answers one slice. The set is what it fills in as it goes, and
// resreg.invoke is what sends it with the audit_log row beside it.
//
// One batch is what the transaction was for. objstore.GuardedBatch applies every
// write or none of them, so a mutation whose record refuses lands nothing — the
// guarantee `BEGIN`/`COMMIT` gave, on an interface that has no transaction.

import (
	"context"
	"fmt"
	"slices"

	"github.com/kronael/arizuko/objstore"
)

// WriteSet is what a caller has DECIDED to write and has not written.
//
// A caller may read before it decides — reads take no locks and need no batch —
// but it must never read a value this set's own writes produce, because none of
// them has happened yet.
type WriteSet struct {
	writes []objstore.Write
	// refused is what a refused batch MEANS to the caller that buffered the one
	// write able to refuse. Held here rather than beside the write because a
	// driver answers a batch with ONE bit (objstore.Store): the set can carry one
	// such meaning and no more.
	refused error
}

// Add buffers writes that cannot refuse — an unconditional Put or Delete, or a
// Merge, which takes no condition at all.
//
// A write whose condition IS the caller's answer goes through AddGuarded, so a
// refusal that means something is never silently read as "the batch lost".
func (w *WriteSet) Add(writes ...objstore.Write) {
	w.writes = append(w.writes, writes...)
}

// AddGuarded buffers ONE write whose condition may refuse, and the error that
// refusal means — the "no such gate", the "already resolved", the "no matching
// grant" a surface answers 404 or 409 with.
//
// A set holds at most one, and a second is an ERROR rather than a silent
// overwrite: the driver reports a refusal as one bit, so two conditions in one
// batch could not be told apart and whichever error was picked would be a guess.
// A caller needing two is a caller whose act is two acts.
func (w *WriteSet) AddGuarded(refused error, write objstore.Write) error {
	if refused == nil {
		return fmt.Errorf("write on %s/%s is guarded and says nothing about what its "+
			"refusal means; an unconditional write goes through Add",
			write.Entity, write.Key.Sort)
	}
	if w.refused != nil {
		return fmt.Errorf("write on %s/%s is the second guarded write of one batch, and a "+
			"batch reports one bit: %v would be indistinguishable from %v",
			write.Entity, write.Key.Sort, refused, w.refused)
	}
	w.refused = refused
	w.writes = append(w.writes, write)
	return nil
}

// Writes answers what the set holds, for a caller that sends the batch itself.
func (w *WriteSet) Writes() []objstore.Write { return w.writes }

// Len is how many writes the set holds. A mutating call that buffered none
// wrote nothing, which several actions legitimately do — a PATCH naming no
// field, a dedup that found the task it was asked to create.
func (w *WriteSet) Len() int { return len(w.writes) }

// Apply sends a caller's whole write set together with the writes named here —
// resreg.invoke's one audit_log row — as ONE batch, so the mutation and the row
// that records it both land or neither does.
//
// A refused batch answers what the guarded write said its refusal means, and
// the generic refusal when nothing did. That second case names the batch rather
// than the write: every scoped batch carries the version marker's Bump, and a
// marker nobody created refuses the whole batch (refusal, scopes.go).
func (s *Store) Apply(ctx context.Context, ws *WriteSet, also ...objstore.Write) error {
	writes := append(slices.Clone(ws.writes), also...)
	if len(writes) == 0 {
		return nil
	}
	committed, err := s.TryWriteEntities(ctx, nil, writes...)
	if err != nil {
		return err
	}
	if committed {
		return nil
	}
	if ws.refused != nil {
		return ws.refused
	}
	return refusal(false, nil, nil, writes)
}
