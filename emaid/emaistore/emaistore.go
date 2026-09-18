// Package emaistore is emaid's storage layer: the adapter asks it what a
// message or a thread needs and it answers, so a second driver reimplements
// this package and no handler changes (spec 6/37 step 1).
// emaid holds no storage outside this package, and there is no SQL driver for
// a statement to run against.
//
// It holds no SQL and no DDL. Every method below is objstore's two primitives —
// Query and GuardedBatch — and the driver is chosen by the daemon that
// constructs this package, never in here (spec 6/37 step 5). An entity is a
// name a driver resolves however it must, so there is no migration chain here:
// creating the storage is asking the driver for the entities this package
// declares, and asking twice is a no-op.
//
// One file holds both entities. `email_msg_ids` is the index that answers
// "which thread does this message belong to", and no operation touches it
// without the thread it points at — so email_threads.go carries both, which is
// 6/37's third rule keeping the write that spans them whole.
package emaistore

import (
	"context"

	"github.com/kronael/arizuko/objstore"
)

// ctx is the context every call here passes the driver. None of this package's
// methods takes one, because none of emaid's callers has one to give: the two
// reads run inside a bot handler and the write runs inside the IMAP poll loop,
// both against a database this adapter owns alone.
func ctx() context.Context { return context.Background() }

// The entities emaid reads.
const (
	// EmailThreads holds one item per conversation: the address to reply to and
	// the Message-ID every reply threads under.
	EmailThreads = "email_threads"
	// EmailMsgIDs holds one item per Message-ID emaid has seen, naming the
	// thread it belongs to. It is the index an inbound In-Reply-To is resolved
	// through.
	EmailMsgIDs = "email_msg_ids"
)

// Entities is the set a driver must be able to serve before emaid boots. It is
// declared here, beside the code that reads them, and applied by whoever holds
// the driver — emaid's own storage.go for the running adapter, the import tool
// for the data move. One declaration, two readers.
var Entities = []string{EmailThreads, EmailMsgIDs}

// key is the coordinate of one item: an identifier emaid is handed, in both
// halves of the key.
//
// THE PARTITION IS THE IDENTIFIER, not a constant naming the daemon. A
// partition is the coordinate a caller FILTERS on (6/37 rule 1) and emaid
// filters on nothing — all three of its reads are point reads, one by
// Message-ID and two by thread id — so no read wants two items co-located and
// nothing is paid for spreading them. What a constant partition would cost is
// growth: `email_msg_ids` gains an item per inbound mail and nothing ever
// sweeps it, so `const Partition = "emaid"` would pile an unbounded index into
// one partition for no read's benefit. That is authd's mistake (6/37 step 3,
// "a single constant partition … every read is a point read or a
// whole-partition scan") without even authd's excuse, because these two
// entities have no bound.
//
// The sort key repeats the identifier because the item has no second
// coordinate: there is exactly one item per id, and DynamoDB refuses an empty
// key attribute, so a blank sort key is not a key on every engine.
func key(id string) objstore.Key { return objstore.Key{Partition: id, Sort: id} }

// Store answers emaid's questions out of the object store it was handed.
type Store struct{ s objstore.Store }

// New wraps a driver. The adapter picks which one; this package never names it,
// which is the whole point of the seam (spec 6/37).
func New(s objstore.Store) *Store { return &Store{s: s} }

// point is the read that addresses ONE item. Every read emaid makes is one of
// these — it holds no predicate, no range and no scan.
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
