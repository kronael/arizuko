package objstore

import (
	"errors"
	"fmt"
)

// ItemCond requires an item to exist, or to not exist, before a batch commits.
type ItemCond uint8

const (
	// ItemAny does not look.
	ItemAny ItemCond = iota
	// ItemPresent requires the item to exist.
	ItemPresent
	// ItemAbsent requires the item to be missing. On a Put that is an insert
	// which refuses to overwrite.
	ItemAbsent
)

// Cond is what one item must look like for the batch to commit. The zero value
// asks nothing. Where's terms are conjuncts, each closing a different hole: a
// refresh claim that dropped its `revoked_at` Absent let a killed family keep
// a live credential (BUGS F36).
//
// A Put carries at most ItemAbsent. A compare-and-swap is an Update, and
// validateWrite refuses a second spelling of it.
type Cond struct {
	Item  ItemCond
	Where []Cmp
}

// WriteOp is what a write does to its item.
type WriteOp uint8

const (
	// Put stores Attrs as the item's whole attribute set, replacing whatever
	// was there. With no Cond it is an upsert; with ItemAbsent it is
	// create-if-absent.
	Put WriteOp = iota
	// Update merges Attrs into the item's existing attributes and takes the
	// attributes named in Remove away. It writes nothing when the item is
	// missing, which its Cond reports as a refusal — that refusal is the
	// compare-and-swap conditional callers depend on. Create-or-merge is Merge.
	Update
	// Merge is Update without must-exist: it merges Attrs, takes Remove away,
	// writes Init only if it CREATES the item, and never refuses (DynamoDB's
	// UpdateItem with `if_not_exists`). Because it never refuses, it can ride
	// as a companion write in someone else's batch (spec 6/40 §"Merge").
	//
	// NOT a Put: an unconditional Put erases every attribute the writer did not
	// name, and a secrets item carries attributes surrogate OAuth owns.
	Merge
	// Delete removes the item. Attrs, Init and Remove are ignored.
	Delete
)

// Write is one mutation, with the condition it must satisfy. Attrs and Remove
// are one statement on every engine; store.RepromptOnboarding clears five
// attributes together because a reset that left one behind would re-greet the
// chat forever (BUGS O1).
type Write struct {
	Op     WriteOp
	Entity string
	Key    Key
	// Attrs are the attributes to set. On a Put they are the item's whole
	// attribute set.
	Attrs map[string]string
	// Init are the attributes an item is BORN with: written when this write
	// creates the item, never when it merges into an existing one, so a
	// `added_at` is not overwritten by the latest write's instant. Only a
	// Merge has them. A Merge carrying ONLY Init is create-if-absent that does
	// not refuse.
	Init map[string]string
	// Remove names attributes to take away; an Update or a Merge has them.
	// Absent is the only null, so this is the only way to make an attribute
	// missing: writing "" leaves one that Present still finds. A name the item
	// does not carry is a no-op.
	Remove []string
	Cond   Cond
}

// Check is a condition on an item the batch DOES NOT modify: a claim on one
// item gating a write to another.
type Check struct {
	Entity string
	Key    Key
	Cond   Cond
}

// MaxBatchActions is how many checks and writes one batch may carry: DynamoDB's
// TransactWriteItems bound, declared with the interface so no driver holds its
// own opinion of it.
const MaxBatchActions = 100

// ValidateBatch refuses a batch no driver may accept; every driver calls it
// before it touches an engine. One action per item is DynamoDB's rule, and the
// reason a Check is on an item the batch may NOT modify. A batch with no
// writes is a caller mistake: reads go through Query.
func ValidateBatch(checks []Check, writes []Write) error {
	if len(writes) == 0 {
		return errors.New("a batch with no writes: read through Query, which takes no locks")
	}
	if n := len(checks) + len(writes); n > MaxBatchActions {
		return fmt.Errorf("batch carries %d actions, more than the %d one transaction may hold",
			n, MaxBatchActions)
	}
	seen := make(itemClaims, len(checks)+len(writes))
	for _, c := range checks {
		if err := seen.claim(c.Entity, c.Key); err != nil {
			return err
		}
		if err := validateCond(c.Entity, c.Cond); err != nil {
			return err
		}
	}
	for _, w := range writes {
		if err := seen.claim(w.Entity, w.Key); err != nil {
			return err
		}
		if err := validateWrite(w); err != nil {
			return err
		}
	}
	return nil
}

// itemClaims records the items one transaction has already acted on.
type itemClaims map[[3]string]bool

// claim records one action on one item and refuses a second.
func (c itemClaims) claim(entity string, k Key) error {
	id := [3]string{entity, k.Partition, k.Sort}
	if c[id] {
		return fmt.Errorf("batch acts twice on %s/%s/%s; one transaction holds at most "+
			"one action per item", entity, k.Partition, k.Sort)
	}
	c[id] = true
	return nil
}

// oneActionPerItem refuses a write set that names an item twice, ahead of any
// batching: the versioned replace splits its writes across transactions, so a
// set that would collide in one batch is refused whatever the split.
func oneActionPerItem(writes []Write) error {
	seen := make(itemClaims, len(writes))
	for _, w := range writes {
		if err := seen.claim(w.Entity, w.Key); err != nil {
			return err
		}
	}
	return nil
}

// validateWrite refuses one mutation no driver may accept.
func validateWrite(w Write) error {
	if err := checkOrderedSort(w.Entity, w.Key.Sort); err != nil {
		return err
	}
	for attr, value := range w.Attrs {
		if err := checkOrderedAttr(w.Entity, attr, value); err != nil {
			return err
		}
	}
	if err := validateCond(w.Entity, w.Cond); err != nil {
		return err
	}
	if w.Op == Put && (len(w.Cond.Where) > 0 || w.Cond.Item == ItemPresent) {
		return fmt.Errorf("put on %s/%s conditions on the item it replaces; a Put carries at "+
			"most ItemAbsent, and a compare-and-swap on an item that must already be there "+
			"is an Update", w.Entity, w.Key.Sort)
	}
	if err := validateMerge(w); err != nil {
		return err
	}
	if len(w.Remove) == 0 {
		return nil
	}
	if w.Op != Update && w.Op != Merge {
		return fmt.Errorf("%s/%s removes attributes on a %s; only an Update or a Merge has a "+
			"REMOVE half, because a Put already replaces the whole document and a Delete takes "+
			"the item", w.Entity, w.Key.Sort, w.Op)
	}
	for _, attr := range w.Remove {
		if attr == "" {
			return fmt.Errorf("update on %s/%s removes an unnamed attribute", w.Entity, w.Key.Sort)
		}
		if _, born := w.Init[attr]; born {
			return fmt.Errorf("merge on %s/%s gives %q to the item it creates and takes it from "+
				"the item it merges into", w.Entity, w.Key.Sort, attr)
		}
		if _, both := w.Attrs[attr]; both {
			// DynamoDB refuses the overlap outright ("Two document paths overlap
			// with each other"), so neither engine guesses.
			return fmt.Errorf("update on %s/%s both sets and removes %q", w.Entity, w.Key.Sort, attr)
		}
	}
	return nil
}

// validateMerge holds the rules Init and Merge carry. A Merge NEVER REFUSES, so
// it takes no condition at all; a conditional create-or-merge is an Update.
func validateMerge(w Write) error {
	for attr, value := range w.Init {
		if err := checkOrderedAttr(w.Entity, attr, value); err != nil {
			return err
		}
		if _, both := w.Attrs[attr]; both {
			return fmt.Errorf("merge on %s/%s gives %q one value on create and another on "+
				"merge; an attribute has one writer per write", w.Entity, w.Key.Sort, attr)
		}
	}
	if w.Op != Merge {
		if len(w.Init) > 0 {
			return fmt.Errorf("%s/%s carries create-only attributes on a %s; only a Merge has "+
				"two halves, because a Put creates every time and an Update never does",
				w.Entity, w.Key.Sort, w.Op)
		}
		return nil
	}
	if w.Cond.Item != ItemAny || len(w.Cond.Where) > 0 {
		return fmt.Errorf("merge on %s/%s carries a condition; a Merge never refuses, and a "+
			"create-or-merge that must be able to refuse is an Update", w.Entity, w.Key.Sort)
	}
	// A Merge naming NO attribute is valid: it creates the item and leaves an
	// existing one alone, and EXISTENCE is the whole fact where the entity is
	// a list (store.putMessage opens a chat that way).
	return nil
}

// validateCond width-checks the values a condition compares against; a wrong
// width on a guard fails as silently as it did on the cursor (BUGS L97).
func validateCond(entity string, c Cond) error {
	for _, cmp := range c.Where {
		if cmp.Op == Absent || cmp.Op == Present {
			continue
		}
		if err := checkOrderedAttr(entity, cmp.Attr, cmp.Value); err != nil {
			return err
		}
	}
	return nil
}

// String names a write op in an error a human reads.
func (o WriteOp) String() string {
	switch o {
	case Put:
		return "Put"
	case Update:
		return "Update"
	case Merge:
		return "Merge"
	case Delete:
		return "Delete"
	default:
		return fmt.Sprintf("WriteOp(%d)", uint8(o))
	}
}
