// Package chanstore is chanlib's storage layer: every channel adapter asks it
// what a delivery needs and it answers, so a second driver reimplements this
// package and no adapter changes (spec 6/37 step 1).
//
// It holds no SQL and no DDL. Every method below is objstore's two primitives —
// Query and GuardedBatch — and the driver is chosen by whoever opens the
// adapter's database, never in here (spec 6/37 step 5). An entity is a name a
// driver resolves however it must, so there is no migration chain: preparing
// the storage is asking the driver for the entities this package declares, and
// asking twice is a no-op.
//
// Three entities, three questions, and they share nothing. `outbound_dedup`
// answers "was this message already sent"; `inbound_pending` answers "which
// messages is this adapter still holding"; `file_urls` answers "what platform
// URL does this proxied attachment id stand for". No operation spans them, so
// they get a file each (6/37 rule 1) and no file needs another's coordinate.
//
// One database per adapter holds all three. chanlib SHIPS the entities; the FILE
// belongs to the adapter daemon, one per service, so an adapter stays
// outage-resistant on its own and no library owns a database.
package chanstore

import (
	"context"

	"github.com/kronael/arizuko/objstore"
)

// The entities every channel adapter reads. An entity is a name a driver
// resolves; what it costs to reach an item inside one is the (partition, sort)
// coordinate each file documents, and the two here made OPPOSITE choices for
// reasons each file states.
const (
	// OutboundDedup holds one item per outbound delivery the adapter was asked
	// to make, addressed by routd's idempotency key.
	OutboundDedup = "outbound_dedup"
	// InboundPending holds one item per inbound message routd has not accepted,
	// addressed by the adapter's own stable message id.
	InboundPending = "inbound_pending"
	// FileURL holds one item per attachment published behind the adapter's
	// `/files/<id>` proxy, addressed by that opaque id.
	FileURL = "file_urls"
)

// Entities is the set a driver must be able to serve before an adapter boots.
// It is declared here, beside the code that reads them, and applied by whoever
// holds the driver — chanlib's opener for a running adapter, import.go for a
// test fixture. One declaration, two readers; an engine with no DDL applies
// nothing and still answers every query below.
var Entities = []string{OutboundDedup, InboundPending, FileURL}

// The two timestamps the sweep COMPARES are declared ordered, so every driver
// refuses a value of another width before it can sort into the wrong place.
//
// Only these two. `outbound_dedup`'s created_at and settled_at are operator
// evidence with no reader — nothing selects, filters or orders on them — and
// declaring a width for them would be a claim about data that already exists:
// the two live teled databases carry 28, 29 and 30-byte values across 110 rows,
// written before core.TS was the layout (BUGS L97). A declaration there would
// refuse rows the live databases already hold, for a guarantee no read asks
// for.
func init() {
	objstore.OrderedAttr(InboundPending, attrNextAttemptAt, tsWidth)
	objstore.OrderedAttr(InboundPending, attrFirstSeenAt, tsWidth)
}

// tsWidth is core.TS's width. A test checks it against core.TS itself, because
// a constant that drifted from the layout would refuse every write instead of
// guarding one.
const tsWidth = 30

// Store answers an adapter's storage questions out of the object store it was
// handed.
type Store struct{ s objstore.Store }

// New wraps a driver. Whoever opens the adapter's database picks which one; this
// package never names it, which is the whole point of the seam (spec 6/37).
func New(s objstore.Store) *Store { return &Store{s: s} }

// point is the read that addresses ONE item and applies no predicate. It answers
// "is it there, and what does it carry".
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
