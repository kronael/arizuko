// Package runstore is runed's storage layer. It holds no SQL and no DDL: every
// method is objstore's two primitives, and the driver is chosen by whoever
// opens runed's store (spec 6/37). Three entities, three partition choices,
// and they disagree on purpose (6/37 rule 5): `spawns` is mutable state
// addressed by run id, so it takes ONE partition and the run id as its sort
// key; `session_log` is history read a folder at a time, so the FOLDER is the
// partition and a time key the sort key; `circuit_breaker` is one flag per
// folder under a compare-and-swap, so the FOLDER is the whole coordinate.
package runstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/kronael/arizuko/objstore"
)

// The entities runed reads.
const (
	// Spawns holds one item per container spawn, addressed by its run id.
	Spawns = "spawns"
	// SessionLog holds one item per agent session, addressed by the folder it
	// ran in and the instant it started.
	SessionLog = "session_log"
	// CircuitBreaker holds one item per folder whose last agent runs failed.
	CircuitBreaker = "circuit_breaker"
)

// Entities is the set a driver must serve before runed boots, applied by
// whoever holds the driver (runed's opener, the import tool).
var Entities = []string{Spawns, SessionLog, CircuitBreaker}

// EntityMaker is a driver that needs its entities declared before they answer.
// An engine with no DDL implements it as a no-op, which is why it is not on
// objstore.Store: schema is driver-local (spec 6/40).
type EntityMaker interface {
	CreateEntity(ctx context.Context, entity string) error
}

// EnsureEntities prepares a database to hold runed's state. Production uses
// store.PrepareOwner (runed/db.go); tests and the importer call this.
// Asking twice is a no-op.
func EnsureEntities(ctx context.Context, m EntityMaker) error {
	for _, entity := range Entities {
		if err := m.CreateEntity(ctx, entity); err != nil {
			return err
		}
	}
	return nil
}

// tsWidth is core.TS's width; a test checks it against core.TS itself.
const tsWidth = 30

// ErrNotFound is the absent item, which runed's HTTP layer answers 404 to.
var ErrNotFound = errors.New("not found")

// Store answers runed's storage questions out of the object store it was
// handed.
type Store struct{ s objstore.Store }

// New wraps a driver.
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
// Each chunk commits on its own: the writes are independent, so a chunk that
// lands and a chunk that does not leave work a re-run finishes.
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
