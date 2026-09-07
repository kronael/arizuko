package store

// entities.go names the tables this package has moved onto objstore, and
// prepares a database to serve them (spec 6/37 step 7).
//
// It is the declaration every reader shares: `migrate` applies it to the
// database it opens, and `arizuko migrate-store` applies it to the database it
// moves. One declaration, two readers — a loop copied into each would be two
// places for the set to drift, which is what BUGS F29 cost when a database
// booted healthy and dropped every audit row.
//
// The set is SMALL because the move is partial. `store.Store` still holds a
// *sql.DB for the tables that have not moved and reaches the entities below
// through the same pool's driver (Store.Obj), so each table has exactly one
// implementation at every moment: a moved one reads through objstore, an
// unmoved one through SQL. There is no fallback and no dual read.

import (
	"context"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The entities this package reads. An entity is a name a driver resolves; what
// it costs to reach an item inside one is the (partition, sort) coordinate each
// file documents.
const (
	// PaneSessions holds one item per Slack assistant pane, addressed by the
	// (team, user, thread) triple that opened it.
	PaneSessions = "pane_sessions"
	// SecretUseLog holds one item per resolved secret a tool call read,
	// addressed by the instant it happened.
	SecretUseLog = "secret_use_log"
)

// Entities is the set a driver must be able to serve before this package
// answers. audit_log is NOT here: four owner databases carry it, `audit` owns
// the declaration, and every opener reaches it through audit.EnsureEntities.
var Entities = []string{PaneSessions, SecretUseLog}

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares a database to hold this package's entities. Two
// openers reach it — store's own migrate and the migration tool — and a loop
// copied into each is two places for the set to drift. Asking twice is a no-op.
//
// audit's entity is NOT created here even though every database that holds
// these also holds one. audit owns that declaration, four owners share it, and
// `arizuko migrate-audit` moves all four in one run — so a caller that needs
// both calls both, the way runed/db.go does. Bundling them made this package's
// migration tool refuse to run until audit's had, on a database where the two
// moves are independent.
func EnsureEntities(ctx context.Context, m EntityMaker) error {
	for _, entity := range Entities {
		if err := m.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return nil
}

// tsWidth is core.TS's width. A test checks it against core.TS itself, because
// a constant that drifted from the layout would refuse every write instead of
// guarding one.
const tsWidth = 30

// setAttr records one optional attribute, leaving it off the item when the
// caller does not carry it. Absent is the only null this model has, so an empty
// string must never be written where a reader tests for the field's presence.
func setAttr(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// applyInChunks sends writes a batch at a time and answers how many landed.
//
// MaxBatchActions is the weakest engine's transaction bound, so a sweep that
// touches more items than one transaction may carry respects it too. Each chunk
// commits on its own: the writes are independent, so a chunk that lands and a
// chunk that does not leave work a re-run finishes rather than a half-applied
// transaction.
func (s *Store) applyInChunks(ctx context.Context, what string,
	writes []objstore.Write) (int, error) {
	done := 0
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := s.Obj().GuardedBatch(ctx, nil, writes[:n])
		if err != nil {
			return done, fmt.Errorf("%s: %w", what, err)
		}
		if !committed {
			return done, fmt.Errorf("%s: a batch of %d was refused, so an item it "+
				"conditions on changed under it", what, n)
		}
		done += n
		writes = writes[n:]
	}
	return done, nil
}
