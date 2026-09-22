package store

// writeset.go holds the sink a resreg handler fills instead of performing its
// writes; resreg.invoke sends the set with its audit_log row as ONE
// GuardedBatch, so a mutation whose record refuses lands nothing.

import (
	"context"
	"fmt"
	"slices"

	"github.com/kronael/arizuko/objstore"
)

// WriteSet is what a caller has DECIDED to write and has not written. A caller
// may read before it decides, but must never read a value this set's own
// writes produce: none of them has happened yet.
type WriteSet struct {
	writes []objstore.Write
	// refused is what a refused batch MEANS to the caller. One per set, because
	// a driver answers a batch with ONE bit (objstore.Store).
	refused error
}

// Add buffers writes that cannot refuse. A write whose condition IS the
// caller's answer goes through AddGuarded.
func (w *WriteSet) Add(writes ...objstore.Write) {
	w.writes = append(w.writes, writes...)
}

// AddGuarded buffers ONE write whose condition may refuse, and the error that
// refusal means. A second is an ERROR: the driver reports one bit, so two
// conditions in one batch could not be told apart.
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

// Len is how many writes the set holds. Several actions legitimately buffer
// none: a PATCH naming no field, a dedup that found the task it was to create.
func (w *WriteSet) Len() int { return len(w.writes) }

// Apply sends the set together with `also` as ONE batch. A refusal answers what
// the guarded write said it means, else the generic refusal naming the batch:
// a marker nobody created refuses the whole batch (refusal, scopes.go).
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
