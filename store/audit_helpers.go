package store

import (
	"context"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// writeAudited sends a mutation and the row that records it as ONE batch, so
// either both land or neither does. build answers the writes it wants and the
// event describing them, and may read before it decides.
func (s *Store) writeAudited(build func() ([]objstore.Write, audit.Event, error)) error {
	writes, ev, err := build()
	if err != nil {
		return err
	}
	if len(writes) == 0 {
		return nil
	}
	_, rec := audit.WriteFor(ev)
	return s.WriteEntities(context.Background(), nil, append(writes, rec)...)
}

// tryWriteAudited is writeAudited where a refused condition is an ANSWER rather
// than a fault. The record rides the same batch, so a refused mutation records
// nothing — there was nothing to record.
func (s *Store) tryWriteAudited(build func() ([]objstore.Write, audit.Event, error)) (bool, error) {
	writes, ev, err := build()
	if err != nil {
		return false, err
	}
	if len(writes) == 0 {
		return false, nil
	}
	_, rec := audit.WriteFor(ev)
	return s.TryWriteEntities(context.Background(), nil, append(writes, rec)...)
}
