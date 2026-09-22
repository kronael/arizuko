package objstore

import "fmt"

// Query is one read. Entity and Partition are always required: a query without
// a partition key is a full scan, which on a key-value engine is a secondary
// index somebody has to declare (6/37 rule 2).
type Query struct {
	Entity    string
	Partition string
	// Key selects inside the partition: everything, one item, a range, or a
	// subtree.
	Key KeyPredicate
	// Where filters on attributes AFTER the key has selected. It is NOT an
	// index: on a key-value engine the page cap is measured before the filter
	// sees the rows, so a selective read belongs in Key.
	Where []Cmp
	// Descending reads the sort key backwards. Ascending is the zero value.
	Descending bool
	// Limit caps the items the read RETURNS. Zero is uncapped, and a read that
	// comes back short of its Limit came back complete.
	Limit int
	// Project names the attributes to return; empty returns all of them. It is
	// a security boundary: authd's SigningKeyMetadata holds because priv_pem is
	// not in its list, and projecting after reading would put the secret on
	// the wire.
	Project []string
}

// KeyOp is how a query selects inside a partition. BETWEEN is not here because
// no caller asks for a two-sided range; it goes on when one does.
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
	// GtKey takes everything after Value: sort > Value. It is the poll loop's
	// cursor read.
	GtKey
	// GeKey takes Value and everything after it: sort >= Value.
	GeKey
	// PrefixKey takes an item and everything below it: begins_with.
	PrefixKey
)

// KeyPredicate selects on the sort key.
//
// PrefixKey is how CONTAINMENT works: a subtree is a string prefix on a path,
// no closure table (6/40 §1). Sep is the separator between a folder and what
// hangs below it ("/" between folders, "#" before a topic); the predicate
// matches Value ITSELF plus everything under Value+Sep, so `a` does not reach
// `ab`. Empty Sep is a bare begins_with.
//
// A SQL driver renders the prefix as a substr comparison, never LIKE: `_` and
// `%` are wildcards, sqlite's LIKE folds ASCII case, and a live instance HAS
// carried a Coach/coach pair (BUGS L66, L122, L252). The ordered operators are
// exact only because an ordered value is fixed-width by declaration
// (ordered.go, BUGS L97).
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
// Value. The comparison is on the attribute's text: core.TS is the layout for
// time because RFC3339Nano strips trailing zeros and every cursor that crossed
// that boundary skipped a row (BUGS L97).
type Cmp struct {
	Attr  string
	Op    Op
	Value string
}

// ValidateQuery refuses a read no driver may accept; every driver calls it
// before it touches an engine.
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

// keyBounds returns the sort-key values a predicate compares against.
func keyBounds(k KeyPredicate) []string {
	switch k.Op {
	case AnyKey:
		return nil
	case PrefixKey:
		// A prefix is NOT width-checked: a subtree read compares a leading
		// fragment, shorter than a whole key by definition.
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
