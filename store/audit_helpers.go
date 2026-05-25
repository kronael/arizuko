package store

// Helpers that wrap "do one mutation + emit one audit row" inside one
// transaction. Used by registry-CRUD callers so the audit row commits
// or rolls back with the mutation. Spec 5/I + audit/PLAN.md.

import (
	"context"
	"database/sql"

	"github.com/kronael/arizuko/audit"
)

// runAudited opens a tx, runs `mutate(tx)`, emits one audit_log row,
// and commits. If `mutate` or the audit emit returns an error, the tx
// rolls back. The Event is built lazily so callers can include
// last-insert-id or affected-rows in the Event.
func (s *Store) runAudited(mutate func(tx *sql.Tx) (audit.Event, error)) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ev, err := mutate(tx)
	if err != nil {
		return err
	}
	if err := audit.EmitInTx(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}
