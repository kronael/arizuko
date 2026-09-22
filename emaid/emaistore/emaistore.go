// Package emaistore is emaid's storage layer. It holds no SQL and no DDL:
// every method is objstore's two primitives, and the daemon that constructs
// this package chooses the driver (spec 6/37).
//
// One file holds both entities. `email_msg_ids` answers "which thread does this
// message belong to", and no operation touches it without the thread it points
// at, so email_threads.go carries both and the write that spans them stays
// whole.
package emaistore

import (
	"context"

	"github.com/kronael/arizuko/objstore"
)

// None of this package's methods takes a context: emaid's two reads run inside
// a bot handler and its write inside the IMAP poll loop, neither of which has
// one to give.
func ctx() context.Context { return context.Background() }

const (
	// EmailThreads: one item per conversation — the reply address and the
	// Message-ID every reply threads under.
	EmailThreads = "email_threads"
	// EmailMsgIDs: one item per Message-ID seen, naming its thread. The index
	// an inbound In-Reply-To resolves through.
	EmailMsgIDs = "email_msg_ids"
)

// Entities is the set a driver must serve before emaid boots. Declared here,
// applied by whoever holds the driver — one declaration, two readers.
var Entities = []string{EmailThreads, EmailMsgIDs}

// key coordinates one item.
//
// THE PARTITION IS THE IDENTIFIER, not a constant naming the daemon. All three
// of emaid's reads are point reads, so nothing wants two items co-located —
// and `email_msg_ids` gains an item per inbound mail that nothing sweeps, so a
// constant partition would pile an unbounded index into one place for no read's
// benefit. The sort key repeats the id because the item has no second
// coordinate and an empty key attribute is not a key on every engine.
func key(id string) objstore.Key { return objstore.Key{Partition: id, Sort: id} }

// Store answers emaid's questions out of the object store it was handed.
type Store struct{ s objstore.Store }

// New wraps a driver. This package never names which.
func New(s objstore.Store) *Store { return &Store{s: s} }

// point reads ONE item: no predicate, no range, no scan.
func (s *Store) point(entity, id string, project ...string) (objstore.Item, bool, error) {
	items, err := s.s.Query(ctx(), objstore.Query{
		Entity: entity, Partition: id,
		Key:     objstore.KeyPredicate{Op: objstore.ExactKey, Value: id},
		Project: project,
	})
	if err != nil || len(items) == 0 {
		return objstore.Item{}, false, err
	}
	return items[0], true, nil
}
