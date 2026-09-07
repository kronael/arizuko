// Package objstore is the object store arizuko addresses instead of per-daemon
// SQL: a daemon names an entity, a coordinate and some attributes, and a driver
// answers. A second engine is a second driver and no daemon changes (spec
// 6/37).
//
// The surface is TWO primitives and nothing else (spec 6/40 §"The seam is TWO
// primitives"). Query reads; GuardedBatch writes under conditions. Everything
// arizuko needs beyond them — the versioned replace, the reachability guard,
// the claim-and-return — is a PROTOCOL written once over the two and shared by
// every driver, never a third method some engine cannot carry.
//
// The interface is deliberately narrower than SQL, because the narrow shape is
// the one every engine has. What a driver must never do is offer more: a
// Postgres driver that quietly gave set-wide atomicity over an unbounded
// predicate would let a caller depend on something a key-value driver cannot
// honour, and the caller would break on the engine nobody tested. objstore's
// conformance suite is what catches that — every driver runs the identical test
// bodies (objstore/conformance).
//
// An attribute value is a string. That is the one modelling rule this package
// imposes, and it is the same discipline core.TS already imposes on time: a
// value that must ORDER is written fixed-width by its writer, so string order
// is the order the caller means, on every engine, with no cast in any dialect.
// A typed attribute model buys ordered numbers and costs a comparison grammar
// per driver — the vendor surface 6/37 §"No surrogate database" refuses.
package objstore

import "context"

// Store is the whole storage surface. A driver implements these two methods and
// nothing else.
type Store interface {
	// Query returns the items q selects, ordered by sort key.
	Query(ctx context.Context, q Query) ([]Item, error)

	// GuardedBatch applies every write, or none of them, and only if every
	// check holds and every write's own condition holds.
	//
	// committed=false with err=nil is a REFUSED batch — a condition did not
	// hold. That is a normal outcome, not a fault: "someone else claimed it"
	// is what a claim reports when it loses, and authd's ClaimRefreshToken
	// returns exactly that today (won=false). A driver that reported it as an
	// error would make every caller unwrap a sentinel to find out whether its
	// own logic worked, and the ones that forgot would log a fault per
	// contended write.
	GuardedBatch(ctx context.Context, checks []Check, writes []Write) (committed bool, err error)
}

// Key is an item's coordinate: the partition it lives in, and its position
// inside that partition.
//
// Partition is the coordinate a caller FILTERS on, not the identifier it looks
// up by (6/37 §"How a daemon's tables become documents", rule 1) — for arizuko
// that is almost always the tenant root, because a folder subtree has to sit
// inside ONE partition for a prefix to cover it (6/40 §1). Choose the folder
// itself and every subtree read becomes a scan.
//
// Sort carries the rest: the path below the tenant, or a fixed-width timestamp
// where the caller reads in time order. Both a prefix predicate and an ordered
// read run on it, so it is the only part of the coordinate that admits a range.
type Key struct {
	Partition string
	Sort      string
}

// Item is one object as a driver returns it. Attrs holds only what the query
// projected; a query with no projection returns every attribute the item has.
//
// An attribute the item does not carry is ABSENT from the map, and absent is
// the only way an attribute is missing — there is no stored null. That is what
// makes Absent and Present exact on every engine, and it is the shape a
// tombstone already has: `used_at IS NULL` in SQL is "the used_at attribute is
// not there".
type Item struct {
	Key   Key
	Attrs map[string]string
}
