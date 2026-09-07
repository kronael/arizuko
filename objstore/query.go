package objstore

import "fmt"

// query.go holds the read shape: what a caller names when it asks a driver for
// items.

// Query is one read. Entity and Partition are always required — a query without
// a partition key is a full scan, and on a key-value engine that is either a
// secondary index somebody has to declare or a design error, never something to
// discover at runtime (6/37 rule 2).
type Query struct {
	Entity    string
	Partition string
	// Key selects inside the partition: everything, one item, a range, or a
	// subtree.
	Key KeyPredicate
	// Where filters on attributes AFTER the key has selected. It narrows a
	// result; it never widens one, and it is NOT an index.
	//
	// "Not an index" is a cost, not a slogan: on a key-value engine a filter
	// that keeps one row in ten thousand still reads ten thousand, because the
	// engine's own page cap is measured before the filter sees them. A
	// selective read belongs in Key, on the sort key, where it costs nothing.
	Where []Cmp
	// Descending reads the sort key backwards. Ascending is the zero value.
	Descending bool
	// Limit caps the items the read RETURNS. Zero is uncapped.
	//
	// It is the caller's bound and the only one there is: a driver answers the
	// whole selection otherwise, so a read that comes back short of its Limit
	// came back complete. With Descending it is the newest-N read, which is the
	// shape a history page asks for.
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
//
// The four inequalities are here because four production reads are made of
// them: the poll loop's cursor (routd/db.go:315), history (routd/reads.go:28),
// bot activity (routd/db.go:1181) and the retry feed (routd/db.go:406). Without
// them every history read is a whole-partition transfer, and the largest chat
// partition measured is 10,688 rows.
//
// DynamoDB's sort-key condition set also carries BETWEEN — "the sort key
// condition must use one of the following comparison operators: =, <, <=, >,
// >=, BETWEEN" plus begins_with
// https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Query.KeyConditionExpressions.html
// — and arizuko has no caller for it. No statement in store, routd, onbod,
// dashd or resreg asks for a two-sided range. It goes on when one does; a
// shipped operator whose only caller is this package's own test suite is the
// accretion 6/40 §"What gets on the surface" refuses.
type KeyOp uint8

const (
	// AnyKey takes the whole partition. It is a Query with no sort condition.
	AnyKey KeyOp = iota
	// ExactKey takes one item: sort = Value.
	ExactKey
	// LtKey takes everything before Value: sort < Value.
	LtKey
	// LeKey takes Value and everything before it: sort <= Value.
	LeKey
	// GtKey takes everything after Value: sort > Value.
	//
	// This is the cursor read. routd's NewMessages is `WHERE timestamp > ?
	// ORDER BY timestamp ASC` and it is the poll loop's watermark, so without
	// this operator the poll loop has no shape on the interface at all.
	GtKey
	// GeKey takes Value and everything after it: sort >= Value.
	GeKey
	// PrefixKey takes an item and everything below it: begins_with.
	PrefixKey
)

// KeyPredicate selects on the sort key.
//
// PrefixKey is how CONTAINMENT works, and containment is the predicate every
// scoped read in arizuko asks (6/40 §1). A subtree is a string prefix on a path
// and nothing else — no closure table, no adjacency list, no nested set.
//
// Sep is the separator between a folder and what hangs below it: "/" between
// folders, "#" before a topic. The predicate matches Value ITSELF plus
// everything under Value+Sep, so `a` does not reach `ab`. Leave Sep empty for a
// bare begins_with over the whole prefix. It means nothing to any other
// operator, and a driver refuses it there rather than ignoring it.
//
// A key-value driver renders the prefix as begins_with, which is exact. A SQL
// driver renders it as a substr comparison, never LIKE: `_` and `%` are LIKE
// wildcards on every engine, so folder `a_b` matches `aXb` on both sqlite
// 3.51.2 and PostgreSQL 17.11. Case folding is an engine difference on top of
// that — sqlite's LIKE folds ASCII and Postgres's does not — and a live
// instance HAS carried a Coach/coach pair (BUGS L66, L122, L252).
//
// The four ordered operators are exact on both, because every stored value is
// TEXT and an ordered one is fixed-width by declaration (ordered.go). That
// declaration is what an ordered key predicate rests on: `timestamp > ?` over
// the seven widths krons' messages table actually carries answers by byte
// order, not by time, which is BUGS L97 read back off live data.
type KeyPredicate struct {
	Op    KeyOp
	Value string
	// Sep is PrefixKey's subtree separator and means nothing to any other
	// operator.
	Sep string
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

// ValidateQuery refuses a read no driver may accept. Every driver calls it
// before it touches an engine, so a malformed predicate is one error message
// rather than one per driver.
func ValidateQuery(q Query) error {
	if q.Entity == "" {
		return fmt.Errorf("query names no entity")
	}
	if q.Partition == "" {
		return fmt.Errorf("query on %s names no partition; a read without one is a full "+
			"scan, which is a secondary index somebody has to declare, never a default", q.Entity)
	}
	if q.Limit < 0 {
		return fmt.Errorf("query on %s asks for %d items", q.Entity, q.Limit)
	}
	if q.Key.Sep != "" && q.Key.Op != PrefixKey {
		return fmt.Errorf("query on %s carries a subtree separator %q on a %s predicate, "+
			"which does not have one", q.Entity, q.Key.Sep, q.Key.Op)
	}
	switch q.Key.Op {
	case AnyKey:
		if q.Key.Value != "" {
			return fmt.Errorf("query on %s carries a key value %q on an AnyKey predicate, "+
				"which selects the whole partition", q.Entity, q.Key.Value)
		}
	case ExactKey, LtKey, LeKey, GtKey, GeKey, PrefixKey:
	default:
		return fmt.Errorf("query on %s uses unknown key predicate %d", q.Entity, q.Key.Op)
	}
	if err := checkOrderedSort(q.Entity, keyBounds(q.Key)...); err != nil {
		return err
	}
	for _, c := range q.Where {
		if c.Op == Absent || c.Op == Present {
			continue
		}
		if err := checkOrderedAttr(q.Entity, c.Attr, c.Value); err != nil {
			return err
		}
	}
	return nil
}

// keyBounds returns the sort-key values a predicate compares against, so the
// width check has one list to walk whatever the operator is.
func keyBounds(k KeyPredicate) []string {
	switch k.Op {
	case AnyKey:
		return nil
	case PrefixKey:
		// A prefix is deliberately NOT width-checked: a subtree read compares a
		// leading fragment of the key, which is shorter than a whole key by
		// definition. begins_with is the one sort-key operator whose operand is
		// not a key.
		return nil
	default:
		return []string{k.Value}
	}
}

// String names a key operator in an error a human reads.
func (o KeyOp) String() string {
	switch o {
	case AnyKey:
		return "AnyKey"
	case ExactKey:
		return "ExactKey"
	case LtKey:
		return "LtKey"
	case LeKey:
		return "LeKey"
	case GtKey:
		return "GtKey"
	case GeKey:
		return "GeKey"
	case PrefixKey:
		return "PrefixKey"
	default:
		return fmt.Sprintf("KeyOp(%d)", uint8(o))
	}
}
