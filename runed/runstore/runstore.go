// Package runstore is runed's storage layer: the daemon asks it what a run, a
// session or a folder's failure streak needs and it answers, so a second driver
// reimplements this package and no handler changes (spec 6/37 step 1).
// runed holds no storage outside this package, and there is no SQL driver for
// a statement to run against.
//
// It holds no SQL and no DDL. Every method below is objstore's two primitives —
// Query and GuardedBatch — and the driver is chosen by whoever opens runed.db,
// never in here (spec 6/37 step 5). An entity is a name a driver resolves
// however it must, so there is no migration chain: preparing the storage is
// asking the driver for the entities this package declares, and asking twice is
// a no-op.
//
// THREE ENTITIES, THREE PARTITION CHOICES, and they disagree on purpose. 6/37
// rule 5 separates history from state and the coordinates follow it:
//
//   - `spawns` is mutable state addressed by run id, so it takes ONE partition
//     and the run id as its sort key (spawns.go says what that costs and what
//     bounds it).
//   - `session_log` is history read a folder at a time, so the FOLDER is the
//     partition and a time key is the sort key (session_log.go).
//   - `circuit_breaker` is one flag per folder under a compare-and-swap, so the
//     FOLDER is the whole coordinate (circuit_breaker.go).
//
// authd took one constant for a whole daemon and 6/37 step 3 writes that up as
// a mistake. Three entities that answer three different questions get three
// answers.
package runstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The entities runed reads. An entity is a name a driver resolves; what it
// costs to reach an item inside one is the (partition, sort) coordinate each
// file documents.
const (
	// Spawns holds one item per container spawn, addressed by its run id.
	Spawns = "spawns"
	// SessionLog holds one item per agent session, addressed by the folder it
	// ran in and the instant it started.
	SessionLog = "session_log"
	// CircuitBreaker holds one item per folder whose last agent runs failed.
	CircuitBreaker = "circuit_breaker"
)

// Entities is the set a driver must be able to serve before runed boots. It is
// declared here, beside the code that reads them, and applied by whoever holds
// the driver — runed's own opener for the running daemon, the import tool for
// the data move. One declaration, two readers; an engine with no DDL applies
// nothing and still answers every query below.
var Entities = []string{Spawns, SessionLog, CircuitBreaker}

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares a database to hold runed's state. Two openers reach
// it — runed's own and the import tool's — and a loop copied into each is two
// places for the set to drift. Asking twice is a no-op.
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

// ErrNotFound is the absent item, which runed's HTTP layer answers 404 to.
var ErrNotFound = errors.New("not found")

// Store answers runed's storage questions out of the object store it was
// handed.
type Store struct{ s objstore.Store }

// New wraps a driver. Whoever opens runed.db picks which one; this package
// never names it, which is the whole point of the seam (spec 6/37).
func New(s objstore.Store) *Store { return &Store{s: s} }

// point is the read that addresses ONE item and applies no predicate. It
// answers "is it there, and what does it carry".
func (s *Store) point(ctx context.Context, entity string, k objstore.Key,
	project ...string) (objstore.Item, bool, error) {
	items, err := s.s.Query(ctx, objstore.Query{
		Entity: entity, Partition: k.Partition,
		Key:     objstore.KeyPredicate{Op: objstore.ExactKey, Value: k.Sort},
		Project: project,
	})
	if err != nil || len(items) == 0 {
		return objstore.Item{}, false, err
	}
	return items[0], true, nil
}

// set records one optional attribute, leaving it off the item when the caller
// does not carry it. Absent is the only null this model has, so an empty string
// must never be written where a reader tests for the field's presence.
func set(attrs map[string]string, name, value string) {
	if value != "" {
		attrs[name] = value
	}
}

// applyInChunks sends writes a batch at a time and answers how many landed.
//
// MaxBatchActions is the weakest engine's transaction bound, so a sweep that
// touches a thousand items respects it too. Each chunk commits on its own: the
// writes are independent, so a chunk that lands and a chunk that does not leave
// work a re-run finishes rather than a half-applied transaction.
func (s *Store) applyInChunks(ctx context.Context, what string, writes []objstore.Write) (int, error) {
	done := 0
	for len(writes) > 0 {
		n := min(len(writes), objstore.MaxBatchActions)
		committed, err := s.s.GuardedBatch(ctx, nil, writes[:n])
		if err != nil {
			return done, err
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
