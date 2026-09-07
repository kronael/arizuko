package objstore

// query.go holds the read shape: what a caller names when it asks a driver for
// items.

// Query is one read. Entity and Partition are always required — a query without
// a partition key is a full scan, and on a key-value engine that is either a
// secondary index somebody has to declare or a design error, never something to
// discover at runtime (6/37 rule 2).
type Query struct {
	Entity    string
	Partition string
	// Key selects inside the partition: everything, one item, or a subtree.
	Key KeyPredicate
	// Where filters on attributes AFTER the key has selected. It narrows a
	// result; it never widens one, and it is not an index.
	Where []Cmp
	// Descending reads the sort key backwards. Ascending is the zero value.
	Descending bool
	// Limit caps the rows returned. Zero is uncapped.
	Limit int
	// Project names the attributes to return; empty returns all of them.
	//
	// It is a security boundary, not an optimisation. authd's
	// SigningKeyMetadata answers the operator's question about a signing key
	// without the private half ever leaving the engine, and it holds because
	// priv_pem is not in its SELECT list. Projecting in the caller after
	// reading the whole item would put the secret on the wire.
	Project []string
}

// KeyOp is how a query selects inside a partition.
type KeyOp uint8

const (
	// AnyKey takes the whole partition.
	AnyKey KeyOp = iota
	// ExactKey takes one item.
	ExactKey
	// PrefixKey takes an item and everything below it.
	PrefixKey
)

// KeyPredicate selects on the sort key. It is exact or prefix and nothing else,
// because those two are what every engine has: a key-value store answers a
// prefix with begins_with on the sort key, natively, under one partition.
//
// PrefixKey is how CONTAINMENT works, and containment is the predicate every
// scoped read in arizuko asks (6/40 §1). A subtree is a string prefix on a path
// and nothing else — no closure table, no adjacency list, no nested set.
//
// Sep is the separator between a folder and what hangs below it: "/" between
// folders, "#" before a topic. The predicate matches Value ITSELF plus
// everything under Value+Sep, so `a` does not reach `ab`. Leave Sep empty for a
// bare begins_with over the whole prefix.
//
// A driver renders this with an exact character comparison, never SQL LIKE:
// LIKE is case-insensitive for ASCII and reads `_` and `%` as wildcards, so
// folder `a_b` matches `aXb` and folder `coach` matches `Coach` — and a live
// instance HAS carried a Coach/coach pair (BUGS L66, L122, L252).
type KeyPredicate struct {
	Op    KeyOp
	Value string
	Sep   string
}

// Op is one comparison.
type Op uint8

const (
	Eq Op = iota
	Ne
	Lt
	Le
	Gt
	Ge
	// Absent holds when the item does not carry the attribute. It is what a
	// tombstone column tests: `used_at IS NULL` is "no used_at attribute".
	Absent
	// Present holds when the item carries the attribute, whatever its value.
	Present
)

// Cmp compares one attribute against one value. Absent and Present ignore
// Value.
//
// The comparison is on the attribute's text, so an ordered comparison means
// what the writer's layout means. core.TS is the layout for time and the
// reason: RFC3339Nano strips trailing zeros, so a whole second sorts AFTER a
// fraction of the same second and every cursor that crossed that boundary
// skipped the row permanently (BUGS L97).
type Cmp struct {
	Attr  string
	Op    Op
	Value string
}
