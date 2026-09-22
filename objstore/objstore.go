// Package objstore is the storage surface arizuko addresses instead of
// per-daemon SQL: a daemon names an entity, a coordinate and some attributes,
// and a driver answers (spec 6/37).
//
// TWO primitives, never a third. Everything else is a protocol written over
// them and shared by every driver (protocol.go, replace.go, consistent.go,
// reach.go). A driver must not offer more, or a caller depends on something
// another engine cannot honour; the conformance suite runs identical bodies
// against every driver to catch that.
//
// An attribute value is a string, and one that must ORDER is written
// fixed-width so string order is the caller's order on every engine. The writer
// declares that width with OrderedAttr (ordered.go).
package objstore

import "context"

type Store interface {
	// The COMPLETE selection, bounded only by q.Limit. A driver that pages
	// does so internally.
	Query(ctx context.Context, q Query) ([]Item, error)

	// Every write or none, and only if every check holds. committed=false with
	// err=nil is a REFUSED batch, a normal outcome — reporting it as an error
	// would make every caller unwrap a sentinel to learn whether its own logic
	// worked.
	GuardedBatch(ctx context.Context, checks []Check, writes []Write) (committed bool, err error)
}

// Key is an item's coordinate. Partition is what a caller FILTERS on, not what
// it looks up by: a folder subtree must sit inside ONE partition for a prefix
// to cover it, so choosing the folder itself turns every subtree read into a
// scan. Sort is the only part that admits a range.
type Key struct {
	Partition string
	Sort      string
}

// Item is one object. An attribute it does not carry is ABSENT — there is no
// stored null, which is what makes Absent and Present exact on every engine.
type Item struct {
	Key   Key
	Attrs map[string]string
}
